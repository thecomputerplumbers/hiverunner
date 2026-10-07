package cache

import (
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// The cache service speaks Twirp: one POST per call, with a JSON body from
// GitHub's own actions and a protobuf body from some other clients. Each
// call is answered in the form it was asked in.

// request is every field any of the three calls sends.
type request struct {
	Key         string   `json:"key"`
	Version     string   `json:"version"`
	RestoreKeys []string `json:"restore_keys"`
	// SizeBytes is a 64-bit integer, which JSON carries as a string.
	SizeBytes int64 `json:"-"`
	hasSize   bool
}

// response is every field any of the three calls answers with.
type response struct {
	OK          bool
	UploadURL   string
	DownloadURL string
	MatchedKey  string
	EntryID     string
	Message     string
}

// call names which of the three a request is, since the same field number
// means different things in each.
type call int

const (
	callCreate call = iota
	callFinalize
	callLookup
)

const maxCallBody = 1 << 20

func isProtobuf(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Content-Type"), "application/protobuf")
}

func readCall(r *http.Request, which call) (request, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxCallBody+1))
	if err != nil {
		return request{}, err
	}
	if len(raw) > maxCallBody {
		return request{}, errors.New("the request is too large")
	}
	if isProtobuf(r) {
		return decodeProtobuf(raw, which)
	}
	var parsed struct {
		request
		// Both spellings: the protobuf field name and its JSON name.
		RestoreKeysCamel []string        `json:"restoreKeys"`
		SizeSnake        json.RawMessage `json:"size_bytes"`
		SizeCamel        json.RawMessage `json:"sizeBytes"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return request{}, errors.New("the request is not valid JSON")
	}
	out := parsed.request
	if len(out.RestoreKeys) == 0 {
		out.RestoreKeys = parsed.RestoreKeysCamel
	}
	for _, size := range []json.RawMessage{parsed.SizeSnake, parsed.SizeCamel} {
		text := strings.Trim(string(size), `"`)
		if text == "" || text == "null" {
			continue
		}
		parsedSize, err := strconv.ParseInt(text, 10, 64)
		if err != nil || parsedSize < 0 {
			return request{}, errors.New("size_bytes is not a size")
		}
		out.SizeBytes, out.hasSize = parsedSize, true
		break
	}
	return out, nil
}

func decodeProtobuf(raw []byte, which call) (request, error) {
	var out request
	for len(raw) > 0 {
		tag, n := binary.Uvarint(raw)
		if n <= 0 {
			return request{}, errors.New("the request is not valid protobuf")
		}
		raw = raw[n:]
		field, wire := int(tag>>3), tag&7
		switch wire {
		case 0:
			value, n := binary.Uvarint(raw)
			if n <= 0 {
				return request{}, errors.New("the request is not valid protobuf")
			}
			raw = raw[n:]
			if which == callFinalize && field == 3 {
				if value > 1<<62 {
					return request{}, errors.New("size_bytes is not a size")
				}
				out.SizeBytes, out.hasSize = int64(value), true
			}
		case 2:
			length, n := binary.Uvarint(raw)
			if n <= 0 || length > uint64(len(raw)-n) {
				return request{}, errors.New("the request is not valid protobuf")
			}
			value := string(raw[n : n+int(length)])
			raw = raw[n+int(length):]
			// Field 1 is metadata in all three, which nothing here uses.
			switch {
			case field == 2:
				out.Key = value
			case which == callCreate && field == 3, which != callCreate && field == 4:
				out.Version = value
			case which == callLookup && field == 3:
				out.RestoreKeys = append(out.RestoreKeys, value)
			}
		case 1:
			if len(raw) < 8 {
				return request{}, errors.New("the request is not valid protobuf")
			}
			raw = raw[8:]
		case 5:
			if len(raw) < 4 {
				return request{}, errors.New("the request is not valid protobuf")
			}
			raw = raw[4:]
		default:
			return request{}, errors.New("the request is not valid protobuf")
		}
	}
	return out, nil
}

func writeCall(w http.ResponseWriter, r *http.Request, which call, answer response) {
	if isProtobuf(r) {
		w.Header().Set("Content-Type", "application/protobuf")
		_, _ = w.Write(encodeProtobuf(which, answer))
		return
	}
	body := map[string]any{"ok": answer.OK}
	set := func(name, value string) {
		if value != "" {
			body[name] = value
		}
	}
	switch which {
	case callCreate:
		set("signed_upload_url", answer.UploadURL)
		set("message", answer.Message)
	case callFinalize:
		set("entry_id", answer.EntryID)
		set("message", answer.Message)
	case callLookup:
		set("signed_download_url", answer.DownloadURL)
		set("matched_key", answer.MatchedKey)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func encodeProtobuf(which call, answer response) []byte {
	var out []byte
	text := func(field int, value string) {
		if value == "" {
			return
		}
		out = binary.AppendUvarint(out, uint64(field<<3|2))
		out = binary.AppendUvarint(out, uint64(len(value)))
		out = append(out, value...)
	}
	if answer.OK {
		out = append(out, 1<<3|0, 1)
	}
	switch which {
	case callCreate:
		text(2, answer.UploadURL)
		text(3, answer.Message)
	case callFinalize:
		if id, err := strconv.ParseUint(answer.EntryID, 10, 63); err == nil && id > 0 {
			out = binary.AppendUvarint(out, 2<<3|0)
			out = binary.AppendUvarint(out, id)
		}
		text(3, answer.Message)
	case callLookup:
		text(2, answer.DownloadURL)
		text(3, answer.MatchedKey)
	}
	return out
}

// writeFault answers a call that could not be served, in Twirp's error
// form. The client treats it as a failed cache step, never a failed job.
func writeFault(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "msg": message})
}

// blockList reads the list that ends a block upload: the ids of the blocks
// that make up the archive, in order.
func blockList(body io.Reader) ([]string, error) {
	decoder := xml.NewDecoder(io.LimitReader(body, 16<<20))
	var ids []string
	var inside bool
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the block list is not valid XML")
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "BlockList":
				inside = true
			case "Latest", "Uncommitted", "Committed":
				if !inside {
					return nil, fmt.Errorf("the block list is malformed")
				}
				var id string
				if err := decoder.DecodeElement(&id, &element); err != nil {
					return nil, fmt.Errorf("the block list is not valid XML")
				}
				if id = strings.TrimSpace(id); id == "" {
					return nil, fmt.Errorf("the block list names an empty block")
				}
				ids = append(ids, id)
			}
		}
	}
	if !inside || len(ids) == 0 {
		return nil, fmt.Errorf("the block list is empty")
	}
	return ids, nil
}
