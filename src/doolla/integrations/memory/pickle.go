package memory

// Doolla persists FAISS metadata as a Python dictionary of JSON-like values.
// Decode only scalar/container opcodes; never import or invoke Python objects.
import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

type marker struct{}
type pointClass struct{}
type emptyTuple struct{}
type pointState struct{ dict fields }
type pickleList []any
type pickleDecoder struct {
	*bytes.Reader
	stack []any
	memo  map[int]any
}

func decodePickle(data []byte) (fields, error) {
	return decodeDictionary(data, false)
}

// pointMode recognizes the fixed Pydantic PointStruct envelope as inert data.
func decodeDictionary(data []byte, pointMode bool) (fields, error) {
	p := pickleDecoder{Reader: bytes.NewReader(data), memo: map[int]any{}}
	for {
		op, err := p.ReadByte()
		if err != nil {
			return nil, err
		}
		switch op {
		case 0x80:
			version, err := p.ReadByte()
			if err != nil || version < 2 || version > 5 {
				return nil, errors.New("unsupported pickle protocol")
			}
		case 0x93: // STACK_GLOBAL: only the outer Qdrant PointStruct marker is allowed.
			if !pointMode || len(p.stack) != 2 || p.stack[0] != "qdrant_client.http.models.models" || p.stack[1] != "PointStruct" {
				return nil, errors.New("unsupported pickle class")
			}
			p.stack = []any{pointClass{}}
		case ')':
			if !pointMode {
				return nil, errors.New("unexpected pickle tuple")
			}
			p.push(emptyTuple{})
		case 0x81: // NEWOBJ: recognize the marker and empty constructor arguments.
			if !pointMode || len(p.stack) != 2 || p.stack[0] != (pointClass{}) || p.stack[1] != (emptyTuple{}) {
				return nil, errors.New("invalid PointStruct marker")
			}
			p.stack = []any{pointState{}}
		case 'b': // BUILD: read the model's field dictionary without executing any code.
			if !pointMode || len(p.stack) != 2 {
				return nil, errors.New("invalid PointStruct state")
			}
			_, isPoint := p.stack[0].(pointState)
			state, ok := p.stack[1].(fields)
			dict, hasDict := state["__dict__"].(fields)
			if !isPoint || !ok || !hasDict {
				return nil, errors.New("invalid PointStruct field dictionary")
			}
			p.stack = []any{pointState{dict}}
		case 0x8f: // Pydantic records which fields were set.
			if !pointMode {
				return nil, errors.New("unexpected pickle set")
			}
			p.push(&pickleList{})
		case 0x95:
			_, err = p.uint(8)
		case '(':
			p.push(marker{})
		case '}':
			p.push(fields{})
		case ']':
			p.push(&pickleList{})
		case 'N':
			p.push(nil)
		case 0x88:
			p.push(true)
		case 0x89:
			p.push(false)
		case 'K', 'M', 'J':
			var n uint64
			n, err = p.uint(map[byte]int{'K': 1, 'M': 2, 'J': 4}[op])
			if op == 'J' {
				p.push(int64(int32(n)))
			} else {
				p.push(int64(n))
			}
		case 0x8a, 0x8b:
			ln := 1
			if op == 0x8b {
				ln = 4
			}
			var n uint64
			n, err = p.uint(ln)
			if err == nil {
				if pointMode && n == 9 {
					var raw [9]byte
					if _, err := io.ReadFull(p, raw[:]); err != nil {
						return nil, err
					}
					if raw[8] != 0 {
						return nil, errors.New("pickle integer exceeds 64 bits")
					}
					p.push(binary.LittleEndian.Uint64(raw[:8]))
					break
				}
				if n > 8 {
					return nil, errors.New("pickle integer exceeds 64 bits")
				}
				var bits uint64
				bits, err = p.uint(int(n))
				if n > 0 && n < 8 && bits&(1<<(n*8-1)) != 0 {
					bits |= ^uint64(0) << (n * 8)
				}
				p.push(int64(bits))
			}
		case 'G':
			var b [8]byte
			_, err = io.ReadFull(p, b[:])
			p.push(math.Float64frombits(binary.BigEndian.Uint64(b[:])))
		case 0x8c, 'X', 0x8d:
			var n uint64
			n, err = p.uint(map[byte]int{0x8c: 1, 'X': 4, 0x8d: 8}[op])
			if err == nil {
				if n > uint64(p.Len()) {
					return nil, io.ErrUnexpectedEOF
				}
				b := make([]byte, n)
				_, err = io.ReadFull(p, b)
				p.push(string(b))
			}
		case 0x94:
			if len(p.stack) == 0 {
				return nil, errors.New("invalid pickle memo")
			}
			p.memo[len(p.memo)] = p.stack[len(p.stack)-1]
		case 'q', 'r', 'h', 'j':
			size := 1
			if op == 'r' || op == 'j' {
				size = 4
			}
			var n uint64
			n, err = p.uint(size)
			if err == nil {
				if op == 'q' || op == 'r' {
					if len(p.stack) == 0 {
						return nil, errors.New("invalid pickle memo")
					}
					p.memo[int(n)] = p.stack[len(p.stack)-1]
				} else {
					v, ok := p.memo[int(n)]
					if !ok {
						return nil, errors.New("missing pickle memo")
					}
					p.push(v)
				}
			}
		case 's', 'u', 'a', 'e', 0x90:
			if op == 0x90 && !pointMode {
				return nil, errors.New("unexpected pickle set items")
			}
			many := op == 'u' || op == 'e' || op == 0x90
			start := len(p.stack) - 1
			if op == 's' {
				start--
			}
			if many {
				start = len(p.stack) - 1
				for start >= 0 {
					if _, ok := p.stack[start].(marker); ok {
						break
					}
					start--
				}
			}
			if start < 1 {
				return nil, errors.New("invalid pickle container")
			}
			target := p.stack[start-1]
			items := p.stack[start:]
			if many {
				items = items[1:]
			}
			if op == 's' || op == 'u' {
				m, ok := target.(map[string]any)
				if !ok || len(items)%2 != 0 {
					return nil, errors.New("invalid pickle dictionary")
				}
				for i := 0; i < len(items); i += 2 {
					var key string
					switch v := items[i].(type) {
					case string:
						key = v
					case int64:
						key = strconv.FormatInt(v, 10)
					default:
						return nil, errors.New("invalid pickle key")
					}
					m[key] = items[i+1]
				}
			} else {
				l, ok := target.(*pickleList)
				if !ok {
					return nil, errors.New("invalid pickle list")
				}
				*l = append(*l, items...)
			}
			p.stack = p.stack[:start]
		case '.':
			if len(p.stack) != 1 {
				return nil, errors.New("invalid pickle stack")
			}
			if pointMode {
				point, ok := p.stack[0].(pointState)
				if !ok || point.dict == nil || p.Len() != 0 {
					return nil, errors.New("invalid PointStruct pickle")
				}
				return point.dict, nil
			}
			m, ok := p.stack[0].(map[string]any)
			if !ok {
				return nil, errors.New("FAISS metadata must be a dictionary")
			}
			return m, nil
		default:
			return nil, fmt.Errorf("unsupported pickle opcode 0x%x", op)
		}
		if err != nil {
			return nil, err
		}
	}
}
func (p *pickleDecoder) push(v any) { p.stack = append(p.stack, v) }
func (p *pickleDecoder) uint(n int) (uint64, error) {
	var b [8]byte
	_, err := io.ReadFull(p, b[:n])
	return binary.LittleEndian.Uint64(b[:]), err
}

// Metadata dictionaries contain scalar, list and map values only.
func encodePickle(rows map[int]fields) ([]byte, error) {
	var b bytes.Buffer
	b.Write([]byte{0x80, 4, '}'})
	for i, d := range rows {
		writeInteger(&b, int64(i))
		raw, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var obj any
		if err := dec.Decode(&obj); err != nil {
			return nil, err
		}
		if err := writePickleValue(&b, obj); err != nil {
			return nil, err
		}
		b.WriteByte('s')
	}
	b.WriteByte('.')
	return b.Bytes(), nil
}
func writeInteger(b *bytes.Buffer, n int64) {
	if n >= 0 && n < 256 {
		b.WriteByte('K')
		b.WriteByte(byte(n))
		return
	}
	b.Write([]byte{0x8a, 8})
	binary.Write(b, binary.LittleEndian, n)
}
func writePickleValue(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteByte('N')
	case bool:
		if x {
			b.WriteByte(0x88)
		} else {
			b.WriteByte(0x89)
		}
	case string:
		b.WriteByte('X')
		binary.Write(b, binary.LittleEndian, uint32(len(x)))
		b.WriteString(x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			writeInteger(b, i)
		} else {
			f, err := x.Float64()
			if err != nil {
				return err
			}
			b.WriteByte('G')
			binary.Write(b, binary.BigEndian, f)
		}
	case []any:
		b.WriteByte(']')
		for _, v := range x {
			if err := writePickleValue(b, v); err != nil {
				return err
			}
			b.WriteByte('a')
		}
	case map[string]any:
		b.WriteByte('}')
		for k, v := range x {
			if err := writePickleValue(b, k); err != nil {
				return err
			}
			if err := writePickleValue(b, v); err != nil {
				return err
			}
			b.WriteByte('s')
		}
	default:
		return fmt.Errorf("unsupported pickle value %T", x)
	}
	return nil
}
