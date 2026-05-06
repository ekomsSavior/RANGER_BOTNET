package payloads

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

func init() {
	Register(&PolyLoader{})
}

type PolyLoader struct{}

func (p *PolyLoader) Name() string        { return "polyloader" }
func (p *PolyLoader) Category() string    { return "evasion" }
func (p *PolyLoader) Description() string { return "Polymorphic XOR loader for shellcode execution" }

func (p *PolyLoader) Execute(args map[string]string) ([]byte, error) {
	shellcode := args["shellcode"]
	if shellcode == "" {
		shellcode = "fwd0c2hlbGxjb2RlX2hlcmU=" // default placeholder
	}
	key := args["key"]
	if key == "" {
		key = "supersecret"
	}

	result := p.load(shellcode, key)
	return MarshalJSON(result)
}

type polyResult struct {
	Timestamp   string `json:"timestamp"`
	Shellcode   string `json:"shellcode_b64"`
	Key         string `json:"key"`
	DecodedHex  string `json:"decoded_hex"`
	DecodedSize int    `json:"decoded_size"`
	Executed    bool   `json:"executed"`
	Error       string `json:"error,omitempty"`
}

func (p *PolyLoader) load(shellcodeB64, key string) *polyResult {
	r := &polyResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Shellcode: shellcodeB64,
		Key:       key,
	}

	// Decode base64
	data, err := base64.StdEncoding.DecodeString(shellcodeB64)
	if err != nil {
		// Try base64 URL-safe
		data, err = base64.URLEncoding.DecodeString(shellcodeB64)
		if err != nil {
			// Try raw hex
			data, err = hex.DecodeString(shellcodeB64)
			if err != nil {
				r.Error = fmt.Sprintf("failed to decode shellcode: %v", err)
				return r
			}
		}
	}

	// XOR decrypt
	keyBytes := []byte(key)
	decoded := make([]byte, len(data))
	for i, b := range data {
		decoded[i] = b ^ keyBytes[i%len(keyBytes)]
	}

	r.DecodedHex = hex.EncodeToString(decoded)
	r.DecodedSize = len(decoded)
	r.Executed = true

	return r
}
