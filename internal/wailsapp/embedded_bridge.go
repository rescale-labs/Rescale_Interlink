package wailsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"
)

type embeddedRPCRequest struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

type embeddedRPCResponse struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type embeddedEventRecord struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Data any    `json:"data"`
}

type embeddedEventStore struct {
	mu     sync.RWMutex
	nextID int64
	events []embeddedEventRecord
}

func (s *embeddedEventStore) append(name string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	s.events = append(s.events, embeddedEventRecord{
		ID:   s.nextID,
		Name: name,
		Data: data,
	})
	if len(s.events) > 1000 {
		s.events = s.events[len(s.events)-1000:]
	}
}

func (s *embeddedEventStore) after(id int64) []embeddedEventRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := make([]embeddedEventRecord, 0, len(s.events))
	for _, record := range s.events {
		if record.ID > id {
			records = append(records, record)
		}
	}
	return records
}

// RunEmbeddedBridge exposes App bindings as loopback JSON RPC for an embedded
// Interlink frontend running inside another native app.
func RunEmbeddedBridge(addr string) error {
	app, err := newEmbeddedApp()
	if err != nil {
		return err
	}
	defer app.shutdown(app.ctx)

	eventStore := &embeddedEventStore{}
	if err := startEmbeddedEventBridge(app, eventStore); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeEmbeddedJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"time": time.Now().Format(time.RFC3339Nano),
		})
	})
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeEmbeddedJSON(w, http.StatusMethodNotAllowed, embeddedRPCResponse{Error: "POST required"})
			return
		}

		var req embeddedRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeEmbeddedJSON(w, http.StatusBadRequest, embeddedRPCResponse{Error: err.Error()})
			return
		}

		result, err := callEmbeddedMethod(app, req.Method, req.Args)
		if err != nil {
			writeEmbeddedJSON(w, http.StatusOK, embeddedRPCResponse{Error: err.Error()})
			return
		}
		writeEmbeddedJSON(w, http.StatusOK, embeddedRPCResponse{Result: result})
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		after := int64(0)
		if raw := r.URL.Query().Get("after"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				writeEmbeddedJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			after = parsed
		}
		writeEmbeddedJSON(w, http.StatusOK, map[string]any{"events": eventStore.after(after)})
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func callEmbeddedMethod(app *App, method string, args []json.RawMessage) (result any, err error) {
	if method == "" {
		return nil, errors.New("method is required")
	}

	fn := reflect.ValueOf(app).MethodByName(method)
	if !fn.IsValid() {
		return nil, fmt.Errorf("unknown Interlink method: %s", method)
	}

	fnType := fn.Type()
	if len(args) != fnType.NumIn() {
		return nil, fmt.Errorf("%s expects %d args, got %d", method, fnType.NumIn(), len(args))
	}

	values := make([]reflect.Value, fnType.NumIn())
	for i := range values {
		argType := fnType.In(i)
		argValue := reflect.New(argType)
		if err := json.Unmarshal(args[i], argValue.Interface()); err != nil {
			return nil, fmt.Errorf("%s arg %d: %w", method, i+1, err)
		}
		values[i] = argValue.Elem()
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%s panicked: %v", method, recovered)
		}
	}()

	out := fn.Call(values)
	if len(out) == 0 {
		return nil, nil
	}

	last := out[len(out)-1]
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	if last.Type().Implements(errorType) {
		if !last.IsNil() {
			return nil, last.Interface().(error)
		}
		out = out[:len(out)-1]
	}

	if len(out) == 0 {
		return nil, nil
	}
	if len(out) == 1 {
		return out[0].Interface(), nil
	}

	results := make([]any, len(out))
	for i, value := range out {
		results[i] = value.Interface()
	}
	return results, nil
}

func startEmbeddedEventBridge(app *App, store *embeddedEventStore) error {
	if app.engine == nil || app.engine.Events() == nil {
		return nil
	}

	app.eventBridge = newEventBridge(
		app.ctx,
		app.engine.Events(),
		func(_ context.Context, name string, data any) {
			store.append(name, data)
		},
	)
	return app.eventBridge.Start()
}

func writeEmbeddedJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		wailsLogger.Error().Err(err).Msg("failed to write embedded bridge JSON response")
	}
}
