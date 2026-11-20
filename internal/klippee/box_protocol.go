package klippee

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	boxVersion        = "KLP_MCU_MKS_V1_1.1.1"
	boxMCU            = "stm32f401xc"
	boxDictionaryHash = "8ae1cedaf7648c75e300ea926de603acf8f48be4b37146b4e677eec7ced62841"
	boxCSR            = 0x40023874
	boxOID            = 0xa5
	boxDebugRead      = "debug_read order=%c addr=%u"
	boxDebugResult    = "debug_result val=%u"
	boxReasonGet      = "reset_reason_get oid=%c"
	boxReasonResult   = "reset_reason_return oid=%c reason=%s"
)

// Klipper frames use reflected CRC-16/CCITT with an initial value of 0xffff.
// https://www.klipper3d.org/Protocol.html
func boxCRC(data []byte) uint16 {
	checksum := uint16(0xffff)
	for _, octet := range data {
		checksum ^= uint16(octet)
		for range 8 {
			if checksum&1 != 0 {
				checksum = checksum>>1 ^ 0x8408
			} else {
				checksum >>= 1
			}
		}
	}
	return checksum
}

func boxFrame(sequence byte, message []byte) []byte {
	frame := append([]byte{byte(len(message) + 5), 0x10 | sequence}, message...)
	crc := boxCRC(frame)
	return append(frame, byte(crc>>8), byte(crc), 0x7e)
}

func boxVLQ(packet []byte, value uint32) []byte {
	for _, bits := range []uint{28, 21, 14, 7} {
		if value >= 3<<(bits-2) {
			packet = append(packet, 0x80|byte(value>>bits)&0x7f)
		}
	}
	return append(packet, byte(value)&0x7f)
}

func readBoxVLQ(packet []byte) (uint32, []byte, error) {
	if len(packet) == 0 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	value := uint32(packet[0] & 0x7f)
	if value&0x60 == 0x60 {
		value |= 0xffffff80
	}
	for i, octet := range packet[:min(len(packet), 5)] {
		if i != 0 {
			value = value<<7 | uint32(octet&0x7f)
		}
		if octet&0x80 == 0 {
			return value, packet[i+1:], nil
		}
	}
	return 0, nil, fmt.Errorf("invalid Klipper integer")
}

type boxWire interface {
	WritePacket(context.Context, []byte) error
	ReadPacket(context.Context) (byte, []byte, error)
}

type boxSession struct {
	wire     boxWire
	sequence byte
}

func (s *boxSession) exchange(ctx context.Context, message []byte, responseID byte, resync bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		if err := s.wire.WritePacket(ctx, boxFrame(s.sequence, message)); err != nil {
			return nil, err
		}
		for {
			seq, payload, err := s.wire.ReadPacket(ctx)
			if err != nil {
				return nil, err
			}
			next := (s.sequence + 1) & 0xf
			if len(payload) == 0 {
				if resync && seq != next && seq != s.sequence {
					s.sequence = seq
					break
				}
				continue
			}
			if seq == next && payload[0] == responseID {
				s.sequence = next
				return payload[1:], nil
			}
		}
	}
	return nil, fmt.Errorf("could not synchronize with QIDI Box")
}

func (s *boxSession) identify(ctx context.Context) ([]byte, error) {
	const segment = 40
	var compressed []byte
	for len(compressed) < 256<<10 {
		offset := uint32(len(compressed))
		payload, err := s.exchange(ctx, append(boxVLQ([]byte{1}, offset), segment), 0, offset == 0)
		if err != nil {
			return nil, fmt.Errorf("identify QIDI Box: %w", err)
		}
		returned, tail, err := readBoxVLQ(payload)
		if err != nil || returned != offset || len(tail) == 0 || int(tail[0]) != len(tail)-1 || int(tail[0]) > segment {
			return nil, fmt.Errorf("invalid QIDI Box identify response")
		}
		compressed = append(compressed, tail[1:]...)
		if tail[0] == 0 {
			reader, err := zlib.NewReader(bytes.NewReader(compressed))
			if err != nil {
				return nil, err
			}
			defer reader.Close()
			raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
			if err != nil || len(raw) > 1<<20 {
				return nil, fmt.Errorf("invalid or oversized QIDI Box dictionary")
			}
			return raw, nil
		}
	}
	return nil, fmt.Errorf("QIDI Box dictionary exceeds collection limit")
}

func validateBoxDictionary(raw []byte) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid QIDI Box command dictionary")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var parsed map[string]any
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(parsed); err != nil {
		return err
	}
	hash := sha256.Sum256(bytes.TrimSuffix(canonical.Bytes(), []byte{'\n'}))
	if fmt.Sprintf("%x", hash) != boxDictionaryHash {
		return fmt.Errorf("unsupported QIDI Box runtime command dictionary")
	}
	var dictionary struct {
		Version   string
		Config    map[string]any
		Commands  map[string]int
		Responses map[string]int
	}
	if err := json.Unmarshal(raw, &dictionary); err != nil {
		return err
	}
	if dictionary.Version != boxVersion || dictionary.Config["MCU"] != boxMCU ||
		dictionary.Commands[boxDebugRead] != 12 || dictionary.Commands[boxReasonGet] != 89 ||
		dictionary.Responses[boxDebugResult] != -30 || dictionary.Responses[boxReasonResult] != -2 {
		return fmt.Errorf("unexpected QIDI Box firmware identity or command IDs")
	}
	return nil
}

func (s *boxSession) readReset(ctx context.Context, dictionaryCheck func([]byte) error, revalidate func() error) (uint32, string, error) {
	dictionary, err := s.identify(ctx)
	if err != nil {
		return 0, "", err
	}
	if err := dictionaryCheck(dictionary); err != nil {
		return 0, "", err
	}
	if err := revalidate(); err != nil {
		return 0, "", err
	}
	// The address, width, command IDs and response correlation are fixed.
	payload, err := s.exchange(ctx, boxVLQ([]byte{12, 2}, boxCSR), 0x62, false)
	if err != nil {
		return 0, "", fmt.Errorf("read QIDI Box reset flags: %w", err)
	}
	flags, tail, err := readBoxVLQ(payload)
	if err != nil || len(tail) != 0 {
		return 0, "", fmt.Errorf("invalid QIDI Box reset-flags response")
	}
	payload, err = s.exchange(ctx, boxVLQ([]byte{89}, boxOID), 0x7e, false)
	if err != nil {
		return 0, "", fmt.Errorf("read QIDI Box reset reason: %w", err)
	}
	oid, tail, err := readBoxVLQ(payload)
	if err != nil || oid != boxOID || len(tail) == 0 || int(tail[0]) != len(tail)-1 {
		return 0, "", fmt.Errorf("invalid QIDI Box reset-reason response")
	}
	reason := string(tail[1:])
	switch reason {
	case "PowerOn", "Unknown", "LowPower", "WindowWatchdog", "Software", "IndependentWatchdog", "NRST_Pin":
		return flags, reason, nil
	default:
		return 0, "", fmt.Errorf("unsupported QIDI Box reset reason")
	}
}

func boxResetResult(endpoint map[string]string, flags uint32, reason string) map[string]any {
	asserted := []string{}
	for i, name := range strings.Fields("LowPower WindowWatchdog IndependentWatchdog Software PowerOn NRST_Pin Brownout") {
		if flags&(1<<(31-i)) != 0 {
			asserted = append(asserted, name)
		}
	}
	address := fmt.Sprintf("0x%08X", boxCSR)
	return map[string]any{
		"schema": "klippee-box-reset-reason/v2", "target_mcu": "mcu_box1", "reason": reason,
		"queried_at": time.Now().UTC().Format(time.RFC3339), "endpoint": endpoint,
		"firmware":    map[string]string{"version": boxVersion, "mcu": boxMCU},
		"reset_flags": map[string]any{"register": "RCC_CSR", "address": address, "raw": flags, "raw_hex": fmt.Sprintf("0x%08X", flags), "asserted": asserted},
		"query": map[string]any{
			"rcc_csr":      map[string]any{"command": boxDebugRead, "response": boxDebugResult, "address": address, "order": 2},
			"reset_reason": map[string]any{"command": boxReasonGet, "response": boxReasonResult, "oid": boxOID},
		},
		"preconditions": map[string]any{"fresh_klippy_state": "shutdown", "endpoint_unowned": true, "exclusive_open": true},
		"safety":        map[string]any{"read_only": true, "reset_requested": false, "flags_cleared": false, "config_changed": false, "klipper_action": "none", "may_be_stale": true},
		"caveat":        "RCC_CSR reset flags are sticky until RMVF is cleared; this read-only query does not clear them and they may predate the incident.",
	}
}
