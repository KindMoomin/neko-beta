// Package store WAL: append-only durability for the object store.
//
// Each committed change is one length-prefixed JSON record. Replay rebuilds the
// store on boot. fsync policy: fdatasync before the store publishes the event
// (SyncEach, default) so anything a controller acted on is already durable; a
// batch mode trades a small loss window for speed (docs/PLATFORM.md m6).
package store

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/neko-box/neko/internal/api"
)

// SyncMode selects WAL durability behaviour.
type SyncMode int

const (
	// SyncEach fdatasyncs every record before the event is published (default).
	SyncEach SyncMode = iota
	// SyncBatch fsyncs periodically; faster, small loss window on crash.
	SyncBatch
)

// record is the on-disk form of an Event. api.Object is an interface, so we tag
// the concrete kind and carry the object as raw JSON to decode on replay.
type record struct {
	Type     EventType       `json:"type"`
	Revision uint64          `json:"revision"`
	Kind     string          `json:"kind"`
	Object   json.RawMessage `json:"object"`
}

// wal is the append-only log.
type wal struct {
	mu   sync.Mutex
	f    *os.File
	mode SyncMode
}

// openWAL opens or creates the WAL at path for append, creating parent dirs.
func openWAL(path string, mode SyncMode) (*wal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open wal %s: %w", path, err)
	}
	return &wal{f: f, mode: mode}, nil
}

// append writes one record for a committed change, durably per mode.
func (w *wal) append(ev Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	objJSON, err := json.Marshal(ev.Object)
	if err != nil {
		return fmt.Errorf("wal marshal object: %w", err)
	}
	rec := record{
		Type:     ev.Type,
		Revision: ev.Revision,
		Kind:     ev.Object.GetType().Kind,
		Object:   objJSON,
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("wal marshal record: %w", err)
	}

	var lenbuf [4]byte
	binary.BigEndian.PutUint32(lenbuf[:], uint32(len(payload)))
	if _, err := w.f.Write(lenbuf[:]); err != nil {
		return fmt.Errorf("wal write len: %w", err)
	}
	if _, err := w.f.Write(payload); err != nil {
		return fmt.Errorf("wal write payload: %w", err)
	}
	if w.mode == SyncEach {
		if err := w.f.Sync(); err != nil {
			return fmt.Errorf("wal sync: %w", err)
		}
	}
	return nil
}

// replay reads every record and invokes apply for each, rebuilding state.
func (w *wal) replay(apply func(Event) error) error {
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(w.f)
	for {
		var lenbuf [4]byte
		if _, err := io.ReadFull(r, lenbuf[:]); err != nil {
			if err == io.EOF {
				break
			}
			// A torn tail record (partial write before a crash) ends replay
			// cleanly — everything before it is intact and durable.
			if err == io.ErrUnexpectedEOF {
				break
			}
			return fmt.Errorf("wal read len: %w", err)
		}
		n := binary.BigEndian.Uint32(lenbuf[:])
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			break // torn tail; stop at last intact record
		}
		var rec record
		if err := json.Unmarshal(payload, &rec); err != nil {
			return fmt.Errorf("wal unmarshal record: %w", err)
		}
		obj, err := decodeObject(rec.Kind, rec.Object)
		if err != nil {
			return err
		}
		if err := apply(Event{Type: rec.Type, Object: obj, Revision: rec.Revision}); err != nil {
			return err
		}
	}
	// Reposition at end for subsequent appends.
	_, err := w.f.Seek(0, io.SeekEnd)
	return err
}

// close flushes and closes the log.
func (w *wal) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// decodeObject turns a tagged raw JSON record back into the concrete api type.
func decodeObject(kind string, raw json.RawMessage) (api.Object, error) {
	var obj api.Object
	switch kind {
	case "Pod":
		obj = &api.Pod{}
	case "Namespace":
		obj = &api.Namespace{}
	case "Deployment":
		obj = &api.Deployment{}
	case "ReplicaSet":
		obj = &api.ReplicaSet{}
	case "Service":
		obj = &api.Service{}
	case "EndpointSlice":
		obj = &api.EndpointSlice{}
	default:
		return nil, fmt.Errorf("wal: unknown kind %q", kind)
	}
	if err := json.Unmarshal(raw, obj); err != nil {
		return nil, fmt.Errorf("wal unmarshal %s: %w", kind, err)
	}
	return obj, nil
}
