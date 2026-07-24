package protocol

import "testing"

// FuzzDecode asserts the one hard invariant of the decoder: it never panics, no
// matter how malformed the input. A crash here is a denial-of-service bug, so
// this fuzzer guards the whole ingest path. The binary codec of iteration 3
// keeps this target; only the seed corpus changes.
func FuzzDecode(f *testing.F) {
	seeds := [][]byte{
		nil,
		[]byte(`{"t":1,"d":{"s":1,"b":2,"a":3}}`),
		[]byte(`{"t":2,"d":{"n":"player"}}`),
		[]byte(`{"t":255,"d":null}`),
		[]byte(`{"t":2,"d":{"n":"`),
		[]byte("\x00\x01\x02\x03"),
		[]byte("{"),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic. The error result is deliberately ignored.
		msg, err := DecodeClient(data)
		if err != nil {
			return
		}
		// A message that decodes must survive a re-encode without error, so a
		// successful decode can never produce something we cannot serialise.
		switch msg.Type {
		case MsgInput:
			if _, err := AppendInput(nil, msg.Input); err != nil {
				t.Fatalf("re-encode input %+v: %v", msg.Input, err)
			}
		case MsgJoin:
			if _, err := AppendJoin(nil, msg.Join); err != nil {
				t.Fatalf("re-encode join %+v: %v", msg.Join, err)
			}
		default:
			t.Fatalf("decoded unexpected type %v from %q", msg.Type, data)
		}
	})
}
