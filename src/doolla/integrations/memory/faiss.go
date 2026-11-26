package memory

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"

	doolla "doolla.ai/sdk"
)

// flat stores the two IndexFlat formats emitted by Python's FAISSAgentMemory.
// Each position in index.faiss corresponds to an integer key in metadata.pkl.
type flat struct{ *remote }
type flatRows struct {
	vectors [][]float32
	docs    map[int]fields
}

func openFlat(c Config) (doolla.MemoryStore, error) {
	if c.Directory == "" {
		c.Directory = "./faiss_index"
	}
	if c.Metric == "" {
		c.Metric = "cosine"
	}
	if c.Metric != "cosine" && c.Metric != "l2" {
		return nil, errors.New("FAISS metric must be cosine or l2")
	}
	if c.Dimension == 0 {
		c.Dimension = 384
	}
	if c.Dimension < 1 || c.Dimension > 1<<20 {
		return nil, errors.New("invalid FAISS dimension")
	}
	if err := os.MkdirAll(c.Directory, 0700); err != nil {
		return nil, err
	}
	f := flat{&remote{Config: c}}
	return &store{f}, nil
}
func (f flat) with(ctx context.Context, write bool, fn func(*flatRows) error) error {
	lock, err := lockFile(ctx, filepath.Join(f.Directory, ".doolla-faiss.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := f.finish(); err != nil {
		return err
	}
	rows, err := f.load()
	if err != nil {
		return err
	}
	if err = fn(rows); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if write {
		return f.persist(rows)
	}
	return nil
}
func (f flat) finish() error {
	pending := filepath.Join(f.Directory, ".doolla-faiss-pending")
	if _, err := os.Stat(pending); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, name := range []string{"index.faiss", "metadata.pkl"} {
		from := filepath.Join(pending, name)
		if err := os.Rename(from, filepath.Join(f.Directory, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Remove(pending)
}
func (f flat) load() (*flatRows, error) {
	idx, ie := os.ReadFile(filepath.Join(f.Directory, "index.faiss"))
	meta, me := os.ReadFile(filepath.Join(f.Directory, "metadata.pkl"))
	rows := &flatRows{docs: map[int]fields{}}
	if os.IsNotExist(ie) && os.IsNotExist(me) {
		return rows, nil
	}
	if ie != nil {
		return nil, ie
	}
	if me != nil {
		return nil, me
	}
	if len(idx) < 45 {
		return nil, errors.New("truncated FAISS header")
	}
	dim := int(binary.LittleEndian.Uint32(idx[4:8]))
	n := binary.LittleEndian.Uint64(idx[8:16])
	nvalues := binary.LittleEndian.Uint64(idx[37:45])
	want := "IxFI"
	if f.Metric == "l2" {
		want = "IxF2"
	}
	if string(idx[:4]) != want || dim != f.Dimension {
		return nil, fmt.Errorf("FAISS index uses %q, dimension %d; configured %s %d", idx[:4], dim, want, f.Dimension)
	}
	if nvalues > uint64(len(idx)-45)/4 || n != nvalues/uint64(dim) || nvalues%uint64(dim) != 0 || uint64(len(idx)-45) != 4*nvalues {
		return nil, errors.New("FAISS vector count mismatch")
	}
	b := idx[45:]
	for i := uint64(0); i < n; i++ {
		v := make([]float32, dim)
		for k := range v {
			v[k] = math.Float32frombits(binary.LittleEndian.Uint32(b))
			b = b[4:]
		}
		rows.vectors = append(rows.vectors, v)
	}
	records, err := decodePickle(meta)
	if err != nil {
		return nil, err
	}
	for k, v := range records {
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i >= len(rows.vectors) {
			return nil, errors.New("metadata index out of range")
		}
		d, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("invalid FAISS metadata")
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var copy fields
		if err := dec.Decode(&copy); err != nil {
			return nil, err
		}
		rows.docs[i] = copy
	}
	return rows, nil
}
func (f flat) persist(rows *flatRows) error {
	var idx bytes.Buffer
	kind := "IxFI"
	metric := uint32(0)
	if f.Metric == "l2" {
		kind = "IxF2"
		metric = 1
	}
	idx.WriteString(kind)
	for _, v := range []any{int32(f.Dimension), int64(len(rows.vectors)), int64(1 << 20), int64(1 << 20), true, metric, uint64(len(rows.vectors)) * uint64(f.Dimension)} {
		binary.Write(&idx, binary.LittleEndian, v)
	}
	for _, v := range rows.vectors {
		binary.Write(&idx, binary.LittleEndian, v)
	}
	meta, err := encodePickle(rows.docs)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(f.Directory, ".doolla-faiss-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, b := range map[string][]byte{"index.faiss": idx.Bytes(), "metadata.pkl": meta} {
		file, err := os.OpenFile(filepath.Join(stage, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(b)
		if err == nil {
			err = file.Sync()
		}
		cerr := file.Close()
		if err != nil {
			return err
		}
		if cerr != nil {
			return cerr
		}
	}
	if err := os.Rename(stage, filepath.Join(f.Directory, ".doolla-faiss-pending")); err != nil {
		return err
	}
	return f.finish()
}
func (f flat) embedding(ctx context.Context, text string) ([]float32, error) {
	v, err := f.vector(ctx, text)
	if err != nil {
		return nil, err
	}
	if f.Metric == "cosine" {
		var n float64
		for _, x := range v {
			n += float64(x) * float64(x)
		}
		if n > 0 {
			normalized := make([]float32, len(v))
			for i, x := range v {
				normalized[i] = x / float32(math.Sqrt(n))
			}
			return normalized, nil
		}
	}
	return v, nil
}
func (f flat) put(ctx context.Context, e entry) error {
	v, err := f.embedding(ctx, textOf(e))
	if err != nil {
		return err
	}
	d, err := document(e, false)
	if err != nil {
		return err
	}
	d["memory_id"] = e.ID
	return f.with(ctx, true, func(rows *flatRows) error {
		for i, prev := range rows.docs {
			if prev["memory_id"] == e.ID {
				rows.docs[i] = d
				rows.vectors[i] = v
				return nil
			}
		}
		rows.docs[len(rows.vectors)] = d
		rows.vectors = append(rows.vectors, v)
		return nil
	})
}
func (f flat) search(ctx context.Context, q query) ([]match, error) {
	v, err := f.embedding(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	var out []match
	err = f.with(ctx, false, func(rows *flatRows) error {
		for i, d := range rows.docs {
			var score float64
			for k, x := range rows.vectors[i] {
				if f.Metric == "cosine" {
					score += float64(x) * float64(v[k])
				} else {
					diff := float64(x) - float64(v[k])
					score += diff * diff
				}
			}
			if f.Metric == "l2" {
				score = 1 / (1 + score)
			}
			if err := collect(&out, "", d, score); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
func (f flat) scan(ctx context.Context, fn func(entry) error) error {
	return f.with(ctx, false, func(rows *flatRows) error {
		for _, d := range rows.docs {
			if err := visit("", d, fn); err != nil {
				return err
			}
		}
		return nil
	})
}
func (f flat) get(ctx context.Context, id string) (*entry, error) {
	var result *entry
	err := f.with(ctx, false, func(rows *flatRows) error {
		for _, d := range rows.docs {
			if d["memory_id"] == id {
				var err error
				result, err = first(d, id)
				return err
			}
		}
		return nil
	})
	return result, err
}
func (f flat) remove(ctx context.Context, id string) error {
	return f.with(ctx, true, func(rows *flatRows) error {
		for i, d := range rows.docs {
			if d["memory_id"] == id {
				delete(rows.docs, i)
				return nil
			}
		}
		return errMissing
	})
}
