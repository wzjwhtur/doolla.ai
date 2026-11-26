package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	doolla "doolla.ai/sdk"
)

// Python QdrantLocal keeps dense vectors in memory and persists PointStructs
// in SQLite. A connection-local key index maps different Python pickle key
// spellings to the same point. The latest row wins, as in CollectionPersistence.
type localQdrant struct {
	*remote
	db *sql.DB
}
type localQdrantStore struct {
	*store
	db        *sql.DB
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}

func (s *localQdrantStore) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.db.Close()
		if s.lock != nil {
			s.closeErr = errors.Join(s.closeErr, s.lock.Close())
		}
	})
	return s.closeErr
}

func openQdrantLocal(ctx context.Context, c Config) (doolla.MemoryStore, error) {
	if c.Collection == "" {
		c.Collection = "tool_memories"
	}
	if !namePattern.MatchString(c.Collection) {
		return nil, errors.New("invalid Qdrant collection name")
	}
	if c.Dimension < 0 {
		return nil, errors.New("invalid Qdrant vector dimension")
	}
	var lock *os.File
	var publishMeta []byte
	dsn := ":memory:"
	if c.Directory != "" && c.Directory != ":memory:" {
		if err := os.MkdirAll(c.Directory, 0700); err != nil {
			return nil, err
		}
		// An embedded client holds its directory for its entire lifetime.
		// Return promptly on contention, like QdrantLocal's nonblocking lock.
		lockCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		var err error
		lock, err = lockFile(lockCtx, filepath.Join(c.Directory, ".lock"))
		cancel()
		if err != nil {
			return nil, fmt.Errorf("open Qdrant local directory lock: %w", err)
		}
		path, meta, err := qdrantStoragePath(&c)
		if err != nil {
			lock.Close()
			return nil, err
		}
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
		dsn, publishMeta = u.String(), meta
	}
	if c.Dimension == 0 {
		c.Dimension = 384
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		if lock != nil {
			lock.Close()
		}
		return nil, err
	}
	db.SetMaxOpenConns(1) // the temporary IDs table is scoped to this connection
	q := &localQdrant{remote: &remote{Config: c}, db: db}
	s := &localQdrantStore{store: &store{driver: q}, db: db, lock: lock}
	if err := q.load(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if publishMeta != nil {
		if err := publishQdrantMeta(c.Directory, publishMeta); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

func qdrantStoragePath(c *Config) (string, []byte, error) {
	dir, err := filepath.Abs(c.Directory)
	if err != nil {
		return "", nil, err
	}
	meta := map[string]json.RawMessage{"aliases": json.RawMessage("{}")}
	path := filepath.Join(dir, "meta.json")
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	collections := map[string]json.RawMessage{}
	if err == nil {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return "", nil, err
		}
		if err := json.Unmarshal(meta["collections"], &collections); err != nil || collections == nil {
			return "", nil, errors.New("invalid Qdrant collection metadata")
		}
		var aliases map[string]string
		if err := json.Unmarshal(meta["aliases"], &aliases); err != nil {
			return "", nil, err
		}
		if target, ok := aliases[c.Collection]; ok {
			if !namePattern.MatchString(target) || collections[target] == nil {
				return "", nil, errors.New("invalid Qdrant collection alias")
			}
			c.Collection = target
		}
	}
	var publish []byte
	if config, exists := collections[c.Collection]; exists {
		var schema struct {
			Vectors struct {
				Size        int
				Distance    string
				Multivector json.RawMessage `json:"multivector_config"`
			}
		}
		if err := json.Unmarshal(config, &schema); err != nil {
			return "", nil, err
		}
		v := schema.Vectors
		if v.Size < 1 || v.Distance != "Cosine" || (len(v.Multivector) > 0 && string(v.Multivector) != "null") {
			return "", nil, errors.New("Qdrant local memory requires a dense unnamed cosine vector")
		}
		if c.Dimension != 0 && c.Dimension != v.Size {
			return "", nil, fmt.Errorf("Qdrant collection dimension %d differs from configured dimension %d", v.Size, c.Dimension)
		}
		c.Dimension = v.Size
	} else {
		if c.Dimension == 0 {
			c.Dimension = 384
		}
		collections[c.Collection], err = json.Marshal(fields{"vectors": fields{"size": c.Dimension, "distance": "Cosine"}})
		if err != nil {
			return "", nil, err
		}
		meta["collections"], err = json.Marshal(collections)
		if err != nil {
			return "", nil, err
		}
		publish, err = json.Marshal(meta)
		if err != nil {
			return "", nil, err
		}
	}
	collectionDir := filepath.Join(dir, "collection", c.Collection)
	// Using a rooted filesystem prevents a redirected collection directory
	// from creating storage outside this Qdrant directory.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Join("collection", c.Collection), 0700); err != nil {
		return "", nil, err
	}
	path = filepath.Join(collectionDir, "storage.sqlite")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		old, err := filepath.Glob(filepath.Join(collectionDir, "storage.dbm*"))
		if err != nil {
			return "", nil, err
		}
		if len(old) > 0 {
			return "", nil, errors.New("legacy Qdrant dbm collection must be migrated to SQLite first")
		}
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("Qdrant storage.sqlite cannot be a symlink")
	}
	return path, publish, nil
}
func publishQdrantMeta(directory string, data []byte) error {
	f, err := os.CreateTemp(directory, ".qdrant-meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(directory, "meta.json"))
}
func (q *localQdrant) load(ctx context.Context) error {
	if _, err := q.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS points (id TEXT PRIMARY KEY, point BLOB);
	CREATE TEMP TABLE doolla_ids (key TEXT PRIMARY KEY, id TEXT NOT NULL);
	CREATE INDEX temp.doolla_qdrant_ids ON doolla_ids(id);`); err != nil {
		return err
	}
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id, point FROM points")
	if err != nil {
		return err
	}
	var keys [][2]string
	for rows.Next() {
		var key string
		var b []byte
		if err := rows.Scan(&key, &b); err != nil {
			rows.Close()
			return err
		}
		p, err := q.decode(b)
		if err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, [2]string{key, p.id})
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range keys {
		if _, err := tx.ExecContext(ctx, "INSERT INTO doolla_ids VALUES (?, ?)", item[0], item[1]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type localQdrantPoint struct {
	id      string
	vector  []float64
	payload fields
}

func (q *localQdrant) decode(raw []byte) (localQdrantPoint, error) {
	var out localQdrantPoint
	d, err := decodeDictionary(raw, true)
	if err != nil {
		return out, err
	}
	var id string
	switch x := d["id"].(type) {
	case string:
		id = x
	case int64:
		id = strconv.FormatInt(x, 10)
	case uint64:
		id = strconv.FormatUint(x, 10)
	default:
		return out, errors.New("invalid Qdrant point ID")
	}
	out.id, _, err = qdrantPointID(id)
	if err != nil {
		return out, err
	}
	v, ok := d["vector"].(*pickleList)
	if !ok || len(*v) != q.Dimension {
		return out, fmt.Errorf("Qdrant local point %s has incompatible vector", out.id)
	}
	out.vector = make([]float64, len(*v))
	for i, val := range *v {
		switch x := val.(type) {
		case float64:
			out.vector[i] = x
		case int64:
			out.vector[i] = float64(x)
		default:
			return out, errors.New("invalid Qdrant vector value")
		}
		if math.IsNaN(out.vector[i]) || math.IsInf(out.vector[i], 0) {
			return out, errors.New("invalid Qdrant vector value")
		}
	}
	// Convert the pickle decoder's lists to ordinary JSON arrays for callers.
	raw, err = json.Marshal(d["payload"])
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	err = dec.Decode(&out.payload)
	return out, err
}
func qdrantPointID(id string) (string, any, error) {
	if i, err := strconv.ParseUint(id, 10, 64); err == nil && len(id) != 32 {
		return strconv.FormatUint(i, 10), i, nil
	}
	s := strings.ToLower(strings.TrimPrefix(strings.TrimSuffix(id, "}"), "{"))
	s = strings.ReplaceAll(strings.TrimPrefix(s, "urn:uuid:"), "-", "")
	if len(s) != 32 {
		return "", nil, errors.New("Qdrant point ID must be a UUID or unsigned integer")
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", nil, errors.New("invalid Qdrant UUID")
	}
	id = s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	return id, id, nil
}

// Python keys are base64-encoded protocol-4 pickles. Encode the same
// canonical representation as pickle.dumps() so Python can update directly.
func writeQdrantID(b *bytes.Buffer, id any) {
	if s, ok := id.(string); ok {
		b.WriteByte(0x8c)
		b.WriteByte(byte(len(s)))
		b.WriteString(s)
		b.WriteByte(0x94)
		return
	}
	n := id.(uint64)
	switch {
	case n <= 255:
		b.Write([]byte{'K', byte(n)})
	case n <= 65535:
		b.WriteByte('M')
		binary.Write(b, binary.LittleEndian, uint16(n))
	case n <= math.MaxInt32:
		b.WriteByte('J')
		binary.Write(b, binary.LittleEndian, uint32(n))
	default:
		var raw [9]byte
		binary.LittleEndian.PutUint64(raw[:], n)
		size := 8
		for size > 1 && raw[size-1] == 0 {
			size--
		}
		if raw[size-1]&0x80 != 0 {
			size++
		}
		b.Write([]byte{0x8a, byte(size)})
		b.Write(raw[:size])
	}
}
func qdrantKey(id any) string {
	var value, b bytes.Buffer
	writeQdrantID(&value, id)
	value.WriteByte('.')
	b.Write([]byte{0x80, 4})
	if value.Len() >= 4 {
		b.WriteByte(0x95)
		binary.Write(&b, binary.LittleEndian, uint64(value.Len()))
	}
	b.Write(value.Bytes())
	return base64.StdEncoding.EncodeToString(b.Bytes())
}
func encodeQdrantPoint(id any, vector []float32, payload fields) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body fields
	if err := dec.Decode(&body); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write([]byte{0x80, 4})
	writePickleValue(&b, "qdrant_client.http.models.models")
	writePickleValue(&b, "PointStruct")
	b.Write([]byte{0x93, ')', 0x81, '}'})
	writePickleValue(&b, "__dict__")
	b.WriteByte('}')
	writePickleValue(&b, "id")
	writeQdrantID(&b, id)
	b.WriteByte('s')
	writePickleValue(&b, "vector")
	b.WriteByte(']')
	for _, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, errors.New("invalid Qdrant vector value")
		}
		b.WriteByte('G')
		binary.Write(&b, binary.BigEndian, float64(v))
		b.WriteByte('a')
	}
	b.WriteByte('s')
	writePickleValue(&b, "payload")
	if err := writePickleValue(&b, body); err != nil {
		return nil, err
	}
	b.WriteString("ss")
	writePickleValue(&b, "__pydantic_fields_set__")
	b.Write([]byte{0x8f, '('})
	for _, k := range []string{"id", "vector", "payload"} {
		writePickleValue(&b, k)
	}
	b.Write([]byte{0x90, 's'})
	for _, k := range []string{"__pydantic_extra__", "__pydantic_private__"} {
		writePickleValue(&b, k)
		b.WriteString("Ns")
	}
	b.WriteString("b.")
	return b.Bytes(), nil
}

func (q *localQdrant) put(ctx context.Context, e entry) error {
	id, value, err := qdrantPointID(e.ID)
	if err != nil {
		return err
	}
	v, err := q.vector(ctx, textOf(e))
	if err != nil {
		return err
	}
	p, err := document(e, false)
	if err != nil {
		return err
	}
	raw, err := encodeQdrantPoint(value, v, p)
	if err != nil {
		return err
	}
	return q.replace(ctx, id, qdrantKey(value), raw)
}
func (q *localQdrant) replace(ctx context.Context, id, key string, raw []byte) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM points WHERE id IN (SELECT key FROM doolla_ids WHERE id=?)", id); err != nil {
		return err
	}
	deleted, err := tx.ExecContext(ctx, "DELETE FROM doolla_ids WHERE id=?", id)
	if err != nil {
		return err
	}
	if raw == nil {
		n, err := deleted.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return errMissing
		}
	} else {
		if _, err = tx.ExecContext(ctx, "INSERT INTO points VALUES (?, ?)", key, raw); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO doolla_ids VALUES (?, ?)", key, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (q *localQdrant) remove(ctx context.Context, id string) error {
	canonical, _, err := qdrantPointID(id)
	if err != nil {
		return errMissing
	}
	return q.replace(ctx, canonical, "", nil)
}
func (q *localQdrant) get(ctx context.Context, id string) (*entry, error) {
	canonical, _, err := qdrantPointID(id)
	if err != nil {
		return nil, errMissing
	}
	var raw []byte
	err = q.db.QueryRowContext(ctx, "SELECT point FROM points JOIN doolla_ids ON points.id=key WHERE doolla_ids.id=? ORDER BY points.rowid DESC LIMIT 1", canonical).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errMissing
	}
	if err != nil {
		return nil, err
	}
	p, err := q.decode(raw)
	if err != nil {
		return nil, err
	}
	return first(p.payload, p.id)
}
func (q *localQdrant) walk(ctx context.Context, fn func(localQdrantPoint) error) error {
	rows, err := q.db.QueryContext(ctx, `SELECT point FROM points WHERE rowid IN (SELECT MAX(points.rowid) FROM points JOIN doolla_ids ON points.id=key GROUP BY doolla_ids.id) ORDER BY rowid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		p, err := q.decode(raw)
		if err != nil {
			return err
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return rows.Err()
}
func (q *localQdrant) scan(ctx context.Context, fn func(entry) error) error {
	return q.walk(ctx, func(p localQdrantPoint) error { return visit(p.id, p.payload, fn) })
}
func (q *localQdrant) search(ctx context.Context, x query) ([]match, error) {
	v, err := q.vector(ctx, x.Text)
	if err != nil {
		return nil, err
	}
	var out []match
	err = q.walk(ctx, func(p localQdrantPoint) error {
		var dot, n1, n2 float64
		for i, b := range p.vector {
			a := float64(v[i])
			dot += a * b
			n1 += a * a
			n2 += b * b
		}
		score := 0.0
		if n1 > 0 && n2 > 0 {
			score = dot / math.Sqrt(n1*n2)
		}
		return collect(&out, p.id, p.payload, score)
	})
	return out, err
}
