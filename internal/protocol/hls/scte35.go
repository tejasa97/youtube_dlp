package hls

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	maxSCTE35HexChars     = 8192
	maxSCTE35DecodedSize  = 4096
	maxDaterangeIDLen     = 256
	maxSCTE35Components   = 256
	scte35TableID         = 0xFC
	scte35CmdSpliceNull   = 0x00
	scte35CmdSpliceSched  = 0x04
	scte35CmdSpliceInsert = 0x05
	scte35CmdTimeSignal   = 0x06
	scte35CmdBandwidthRes = 0x07
)

type scte35Direction int

const (
	scte35DirectionOut scte35Direction = iota
	scte35DirectionIn
	scte35DirectionFromCommand
)

type bitReader struct {
	data []byte
	pos  int
	bits int
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data, bits: len(data) * 8}
}

func (r *bitReader) remaining() int {
	return r.bits - r.pos
}

func (r *bitReader) readBits(count int) (uint64, error) {
	if count < 0 || count > 63 || r.remaining() < count {
		return 0, errors.New("truncated SCTE-35 field")
	}
	var value uint64
	for index := 0; index < count; index++ {
		byteIndex := r.pos / 8
		bitIndex := 7 - (r.pos % 8)
		if r.data[byteIndex]&(1<<bitIndex) != 0 {
			value |= 1 << uint(count-index-1)
		}
		r.pos++
	}
	return value, nil
}

func (r *bitReader) skipBits(count int) error {
	if r.remaining() < count {
		return errors.New("truncated SCTE-35 field")
	}
	r.pos += count
	return nil
}

var mpeg2CRCTable [256]uint32

func init() {
	for index := 0; index < 256; index++ {
		crc := uint32(index) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
		mpeg2CRCTable[index] = crc
	}
}

func mpeg2CRC(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, value := range data {
		crc = (crc << 8) ^ mpeg2CRCTable[byte(crc>>24)^value]
	}
	return crc
}

func decodeSCTE35Hex(raw string) ([]byte, error) {
	if len(raw) > maxSCTE35HexChars {
		return nil, errors.New("SCTE-35 payload exceeds hex length bound")
	}
	if !strings.HasPrefix(raw, "0x") {
		return nil, errors.New("SCTE-35 payload must be 0x-prefixed")
	}
	hexBody := raw[2:]
	if hexBody == "" || len(hexBody)%2 != 0 {
		return nil, errors.New("SCTE-35 payload hex length must be even")
	}
	decoded, err := hex.DecodeString(hexBody)
	if err != nil {
		return nil, errors.New("SCTE-35 payload hex is malformed")
	}
	if len(decoded) == 0 || len(decoded) > maxSCTE35DecodedSize {
		return nil, errors.New("SCTE-35 payload decoded size is out of bounds")
	}
	return decoded, nil
}

func validateSCTE35Direction(payload string, mode scte35Direction) (bool, error) {
	section, err := decodeSCTE35Hex(payload)
	if err != nil {
		return false, err
	}
	if len(section) < 14 {
		return false, errors.New("SCTE-35 section is truncated")
	}
	if section[0] != scte35TableID {
		return false, errors.New("SCTE-35 table_id must be 0xFC")
	}
	if section[1]&0x80 != 0 {
		return false, errors.New("SCTE-35 section_syntax_indicator must be 0")
	}
	if section[1]&0x40 != 0 {
		return false, errors.New("SCTE-35 private_indicator must be 0")
	}
	if section[1]&0x30 != 0x30 {
		return false, errors.New("SCTE-35 section reserved bits must be 0x3")
	}
	sectionLength := int(section[1]&0x0F)<<8 | int(section[2])
	if sectionLength < 4 || 3+sectionLength != len(section) {
		return false, errors.New("SCTE-35 section_length is inconsistent")
	}
	expectedCRC := mpeg2CRC(section[:len(section)-4])
	actualCRC := uint32(section[len(section)-4])<<24 |
		uint32(section[len(section)-3])<<16 |
		uint32(section[len(section)-2])<<8 |
		uint32(section[len(section)-1])
	if expectedCRC != actualCRC {
		return false, errors.New("SCTE-35 CRC-32/MPEG-2 mismatch")
	}

	reader := newBitReader(section[3 : len(section)-4])
	protocolVersion, err := reader.readBits(8)
	if err != nil {
		return false, err
	}
	if protocolVersion != 0 {
		return false, errors.New("unsupported SCTE-35 protocol version")
	}
	encrypted, err := reader.readBits(1)
	if err != nil {
		return false, err
	}
	if encrypted != 0 {
		return false, errors.New("encrypted SCTE-35 packets are unsupported")
	}
	if err := reader.skipBits(6); err != nil { // encryption_algorithm
		return false, err
	}
	if err := reader.skipBits(33); err != nil { // pts_adjustment
		return false, err
	}
	if err := reader.skipBits(8); err != nil { // cw_index
		return false, err
	}
	if err := reader.skipBits(12); err != nil { // tier
		return false, err
	}
	commandLength64, err := reader.readBits(12)
	if err != nil {
		return false, err
	}
	commandLength := int(commandLength64)
	commandType64, err := reader.readBits(8)
	if err != nil {
		return false, err
	}
	commandType := byte(commandType64)
	if reader.pos%8 != 0 {
		return false, errors.New("misaligned SCTE-35 splice command")
	}
	commandStart := reader.pos / 8
	commandEnd := commandStart + commandLength
	body := section[3 : len(section)-4]
	if commandEnd > len(body) {
		return false, errors.New("SCTE-35 splice command exceeds section")
	}
	command := body[commandStart:commandEnd]
	descriptorReader := newBitReader(body[commandEnd:])
	descriptorLength64, err := descriptorReader.readBits(16)
	if err != nil {
		return false, errors.New("SCTE-35 descriptor_loop_length is missing")
	}
	descriptorLength := int(descriptorLength64)
	if descriptorLength > descriptorReader.remaining()/8 {
		return false, errors.New("SCTE-35 descriptor loop exceeds section")
	}
	if err := descriptorReader.skipBits(descriptorLength * 8); err != nil {
		return false, err
	}
	if descriptorReader.remaining() != 0 {
		return false, errors.New("trailing bytes in SCTE-35 section")
	}

	switch mode {
	case scte35DirectionFromCommand:
		if commandType != scte35CmdSpliceInsert {
			return false, errors.New("SCTE35-CMD requires splice_insert")
		}
		out, err := parseSpliceInsertOutOfNetwork(command)
		if err != nil {
			return false, err
		}
		return out, nil
	default:
		switch commandType {
		case scte35CmdSpliceInsert:
			out, err := parseSpliceInsertOutOfNetwork(command)
			if err != nil {
				return false, err
			}
			if mode == scte35DirectionOut && !out {
				return false, errors.New("SCTE35-OUT payload is not an out-of-network splice")
			}
			if mode == scte35DirectionIn && out {
				return false, errors.New("SCTE35-IN payload is not an in-network splice")
			}
			return mode == scte35DirectionOut, nil
		case scte35CmdTimeSignal:
			if err := parseTimeSignal(command); err != nil {
				return false, err
			}
			return mode == scte35DirectionOut, nil
		default:
			return false, errors.New("unsupported SCTE-35 splice command")
		}
	}
}

func parseSpliceInsertOutOfNetwork(command []byte) (bool, error) {
	reader := newBitReader(command)
	if _, err := reader.readBits(32); err != nil { // splice_event_id
		return false, err
	}
	cancel, err := reader.readBits(1)
	if err != nil {
		return false, err
	}
	if cancel != 0 {
		return false, errors.New("cancelled SCTE-35 splice_insert")
	}
	if err := reader.skipBits(7); err != nil { // reserved
		return false, err
	}
	outOfNetwork, err := reader.readBits(1)
	if err != nil {
		return false, err
	}
	programSplice, err := reader.readBits(1)
	if err != nil {
		return false, err
	}
	durationFlag, err := reader.readBits(1)
	if err != nil {
		return false, err
	}
	immediateFlag, err := reader.readBits(1)
	if err != nil {
		return false, err
	}
	if err := reader.skipBits(4); err != nil { // reserved
		return false, err
	}
	if programSplice != 0 {
		if immediateFlag == 0 {
			if err := parseSpliceTime(reader); err != nil {
				return false, err
			}
		}
	} else {
		componentCount64, err := reader.readBits(8)
		if err != nil {
			return false, err
		}
		componentCount := int(componentCount64)
		if componentCount > maxSCTE35Components {
			return false, errors.New("SCTE-35 component_count exceeds bound")
		}
		for index := 0; index < componentCount; index++ {
			if err := reader.skipBits(8); err != nil { // component_tag
				return false, err
			}
			if immediateFlag == 0 {
				if err := parseSpliceTime(reader); err != nil {
					return false, err
				}
			}
		}
	}
	if durationFlag != 0 {
		if err := parseBreakDuration(reader); err != nil {
			return false, err
		}
	}
	if err := reader.skipBits(16 + 8 + 8); err != nil { // unique_program_id, avail_num, avails_expected
		return false, err
	}
	if reader.remaining() != 0 {
		return false, errors.New("trailing bytes in splice_insert command")
	}
	return outOfNetwork != 0, nil
}

func parseTimeSignal(command []byte) error {
	reader := newBitReader(command)
	if err := parseSpliceTime(reader); err != nil {
		return err
	}
	if reader.remaining() != 0 {
		return errors.New("trailing bytes in time_signal command")
	}
	return nil
}

func parseSpliceTime(reader *bitReader) error {
	timeSpecified, err := reader.readBits(1)
	if err != nil {
		return err
	}
	if timeSpecified != 0 {
		if err := reader.skipBits(6); err != nil { // reserved
			return err
		}
		return reader.skipBits(33) // pts_time
	}
	return reader.skipBits(7) // reserved
}

func parseBreakDuration(reader *bitReader) error {
	if err := reader.skipBits(1); err != nil { // auto_return
		return err
	}
	if err := reader.skipBits(6); err != nil { // reserved
		return err
	}
	return reader.skipBits(33) // duration
}

func parseAttributesNoDuplicates(input string) (map[string]string, map[string]bool, error) {
	result := make(map[string]string)
	present := make(map[string]bool)
	for index := 0; index < len(input); {
		start := index
		for index < len(input) && input[index] != '=' {
			index++
		}
		if index == len(input) {
			return nil, nil, fmt.Errorf("attribute %q has no value", input[start:])
		}
		key := strings.TrimSpace(input[start:index])
		index++
		var value string
		if index < len(input) && input[index] == '"' {
			index++
			start = index
			for index < len(input) && input[index] != '"' {
				index++
			}
			if index == len(input) {
				return nil, nil, errors.New("unterminated quoted attribute")
			}
			value = input[start:index]
			index++
		} else {
			start = index
			for index < len(input) && input[index] != ',' {
				index++
			}
			value = strings.TrimSpace(input[start:index])
		}
		if key == "" {
			return nil, nil, errors.New("empty attribute name")
		}
		if present[key] {
			return nil, nil, fmt.Errorf("duplicate attribute %q", key)
		}
		present[key] = true
		result[key] = value
		if index < len(input) {
			if input[index] != ',' {
				return nil, nil, fmt.Errorf("unexpected attribute character %q", input[index])
			}
			index++
		}
	}
	return result, present, nil
}

const daterangeTagPrefix = "#EXT-X-DATERANGE:"

func applyDaterangeSCTE35(rawLine string) (start, end, handled bool, err error) {
	if rawLine == "" || rawLine[0] == ' ' || rawLine[0] == '\t' {
		return false, false, false, nil
	}
	line := strings.TrimRight(rawLine, " \t")
	if !strings.HasPrefix(line, daterangeTagPrefix) {
		return false, false, false, nil
	}
	handled = true
	attributes, present, err := parseAttributesNoDuplicates(line[len(daterangeTagPrefix):])
	if err != nil {
		return false, false, true, err
	}

	hasOut := present["SCTE35-OUT"]
	hasIn := present["SCTE35-IN"]
	hasCMD := present["SCTE35-CMD"]
	directional := 0
	if hasOut {
		directional++
	}
	if hasIn {
		directional++
	}
	if hasCMD {
		directional++
	}
	if directional == 0 {
		return false, false, true, nil
	}
	if directional > 1 {
		return false, false, true, errors.New("ambiguous SCTE-35 directional attributes")
	}

	id := attributes["ID"]
	if id == "" || len(id) > maxDaterangeIDLen {
		return false, false, true, errors.New("daterange ID is missing or exceeds bound")
	}

	var payload string
	var mode scte35Direction
	switch {
	case hasOut:
		payload = attributes["SCTE35-OUT"]
		mode = scte35DirectionOut
	case hasIn:
		payload = attributes["SCTE35-IN"]
		mode = scte35DirectionIn
	default:
		payload = attributes["SCTE35-CMD"]
		mode = scte35DirectionFromCommand
	}
	if payload == "" {
		return false, false, true, errors.New("SCTE-35 directional attribute value is empty")
	}
	out, err := validateSCTE35Direction(payload, mode)
	if err != nil {
		return false, false, true, err
	}
	if out {
		return true, false, true, nil
	}
	return false, true, true, nil
}
