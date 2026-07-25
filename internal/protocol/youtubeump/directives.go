package youtubeump

import (
	"fmt"
)

// Field numbers from LuanRT/googlevideo sabr_redirect.proto,
// sabr_context_update.proto, and sabr_context_sending_policy.proto at commit
// d2fa40d761034a286cf60ee033653307a1295b0c.
const (
	fSabrRedirectURL uint64 = 1

	fSabrContextType          uint64 = 1
	fSabrContextScope         uint64 = 2
	fSabrContextValue         uint64 = 3
	fSabrContextSendByDefault uint64 = 4
	fSabrContextWritePolicy   uint64 = 5

	fSabrSendingPolicyStart   uint64 = 1
	fSabrSendingPolicyStop    uint64 = 2
	fSabrSendingPolicyDiscard uint64 = 3

	fStreamerSabrContextType  uint64 = 1
	fStreamerSabrContextValue uint64 = 2
)

// SabrContextScope values from sabr_context_update.proto.
const (
	SabrContextScopeUnknown       int32 = 0
	SabrContextScopePlayback      int32 = 1
	SabrContextScopeRequest       int32 = 2
	SabrContextScopeWatchEndpoint int32 = 3
	SabrContextScopeContentAds    int32 = 4
)

// SabrContextWritePolicy values from sabr_context_update.proto.
const (
	SabrContextWriteUnspecified  int32 = 0
	SabrContextWriteOverwrite    int32 = 1
	SabrContextWriteKeepExisting int32 = 2
)

type sabrRedirectDirective struct {
	URL string
}

type sabrContextUpdateDirective struct {
	Type          int32
	Scope         int32
	Value         []byte
	SendByDefault bool
	WritePolicy   int32
}

type sabrContextSendingPolicyDirective struct {
	Start   []int32
	Stop    []int32
	Discard []int32
}

func parseSabrRedirect(payload []byte) (sabrRedirectDirective, error) {
	var (
		directive sabrRedirectDirective
		seenURL   bool
	)
	reader := fieldReader{data: payload}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fSabrRedirectURL:
			if wireType != wireBytes {
				return sabrRedirectDirective{}, fmt.Errorf("%w: wrong wire type %d for redirect url", ErrInvalidProtobuf, wireType)
			}
			if seenURL {
				return sabrRedirectDirective{}, fmt.Errorf("%w: duplicate redirect url", ErrInvalidProtobuf)
			}
			seenURL = true
			raw := reader.bytes()
			if reader.err != nil {
				return sabrRedirectDirective{}, reader.err
			}
			if len(raw) == 0 {
				return sabrRedirectDirective{}, fmt.Errorf("%w: empty redirect url", ErrUnsafeRedirect)
			}
			if len(raw) > MaxRedirectURLBytes {
				return sabrRedirectDirective{}, fmt.Errorf("%w: redirect url exceeds bound", ErrUnsafeRedirect)
			}
			directive.URL = string(raw)
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return sabrRedirectDirective{}, reader.err
	}
	if !seenURL {
		return sabrRedirectDirective{}, fmt.Errorf("%w: missing redirect url", ErrUnsafeRedirect)
	}
	if _, err := ValidateSABRURL(directive.URL); err != nil {
		return sabrRedirectDirective{}, fmt.Errorf("%w: %v", ErrUnsafeRedirect, err)
	}
	return directive, nil
}

func parseSabrContextUpdate(payload []byte) (sabrContextUpdateDirective, error) {
	var (
		directive  sabrContextUpdateDirective
		seenType   bool
		seenScope  bool
		seenValue  bool
		seenSend   bool
		seenPolicy bool
	)
	reader := fieldReader{data: payload}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fSabrContextType:
			if wireType != wireVarint {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: wrong wire type %d for context type", ErrInvalidProtobuf, wireType)
			}
			if seenType {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: duplicate context type", ErrInvalidProtobuf)
			}
			seenType = true
			raw := reader.varint()
			if reader.err != nil {
				return sabrContextUpdateDirective{}, reader.err
			}
			value, err := protoInt32FromVarint(raw)
			if err != nil {
				return sabrContextUpdateDirective{}, err
			}
			directive.Type = value
		case num == fSabrContextScope:
			if wireType != wireVarint {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: wrong wire type %d for context scope", ErrInvalidProtobuf, wireType)
			}
			if seenScope {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: duplicate context scope", ErrInvalidProtobuf)
			}
			seenScope = true
			raw := reader.varint()
			if reader.err != nil {
				return sabrContextUpdateDirective{}, reader.err
			}
			value, err := protoInt32FromVarint(raw)
			if err != nil {
				return sabrContextUpdateDirective{}, err
			}
			if value < SabrContextScopeUnknown || value > SabrContextScopeContentAds {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: invalid context scope", ErrInvalidContextState)
			}
			directive.Scope = value
		case num == fSabrContextValue:
			if wireType != wireBytes {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: wrong wire type %d for context value", ErrInvalidProtobuf, wireType)
			}
			if seenValue {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: duplicate context value", ErrInvalidProtobuf)
			}
			seenValue = true
			raw := reader.bytes()
			if reader.err != nil {
				return sabrContextUpdateDirective{}, reader.err
			}
			if len(raw) == 0 {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: empty context value", ErrInvalidContextState)
			}
			if len(raw) > MaxSabrContextValueBytes {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: context value exceeds bound", ErrInvalidContextState)
			}
			directive.Value = append([]byte(nil), raw...)
		case num == fSabrContextSendByDefault:
			if wireType != wireVarint {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: wrong wire type %d for send_by_default", ErrInvalidProtobuf, wireType)
			}
			if seenSend {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: duplicate send_by_default", ErrInvalidProtobuf)
			}
			seenSend = true
			directive.SendByDefault = reader.varint() != 0
		case num == fSabrContextWritePolicy:
			if wireType != wireVarint {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: wrong wire type %d for write_policy", ErrInvalidProtobuf, wireType)
			}
			if seenPolicy {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: duplicate write_policy", ErrInvalidProtobuf)
			}
			seenPolicy = true
			raw := reader.varint()
			if reader.err != nil {
				return sabrContextUpdateDirective{}, reader.err
			}
			value, err := protoInt32FromVarint(raw)
			if err != nil {
				return sabrContextUpdateDirective{}, err
			}
			if value < SabrContextWriteUnspecified || value > SabrContextWriteKeepExisting {
				return sabrContextUpdateDirective{}, fmt.Errorf("%w: invalid write_policy", ErrInvalidContextState)
			}
			directive.WritePolicy = value
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return sabrContextUpdateDirective{}, reader.err
	}
	if !seenType || directive.Type <= 0 {
		return sabrContextUpdateDirective{}, fmt.Errorf("%w: context type must be positive", ErrInvalidContextState)
	}
	if !seenValue {
		return sabrContextUpdateDirective{}, fmt.Errorf("%w: missing context value", ErrInvalidContextState)
	}
	return directive, nil
}

func parseSabrContextSendingPolicy(payload []byte) (sabrContextSendingPolicyDirective, error) {
	var (
		directive sabrContextSendingPolicyDirective
		ops       int
	)
	reader := fieldReader{data: payload}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fSabrSendingPolicyStart:
			values, err := readRepeatedProtoInt32(&reader, wireType, &ops)
			if err != nil {
				return sabrContextSendingPolicyDirective{}, err
			}
			directive.Start = append(directive.Start, values...)
		case num == fSabrSendingPolicyStop:
			values, err := readRepeatedProtoInt32(&reader, wireType, &ops)
			if err != nil {
				return sabrContextSendingPolicyDirective{}, err
			}
			directive.Stop = append(directive.Stop, values...)
		case num == fSabrSendingPolicyDiscard:
			values, err := readRepeatedProtoInt32(&reader, wireType, &ops)
			if err != nil {
				return sabrContextSendingPolicyDirective{}, err
			}
			directive.Discard = append(directive.Discard, values...)
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return sabrContextSendingPolicyDirective{}, reader.err
	}
	for _, values := range [][]int32{directive.Start, directive.Stop, directive.Discard} {
		for _, value := range values {
			if value <= 0 {
				return sabrContextSendingPolicyDirective{}, fmt.Errorf("%w: policy type must be positive", ErrInvalidContextState)
			}
		}
	}
	return directive, nil
}

func readRepeatedProtoInt32(reader *fieldReader, wireType int, ops *int) ([]int32, error) {
	switch wireType {
	case wireVarint:
		if err := bumpCount(ops, 1, MaxSabrContextPolicyOps); err != nil {
			return nil, err
		}
		raw := reader.varint()
		if reader.err != nil {
			return nil, reader.err
		}
		value, err := protoInt32FromVarint(raw)
		if err != nil {
			return nil, err
		}
		return []int32{value}, nil
	case wireBytes:
		packed := reader.bytes()
		if reader.err != nil {
			return nil, reader.err
		}
		return decodePackedProtoInt32(packed, ops)
	default:
		return nil, fmt.Errorf("%w: wrong wire type %d for repeated int32", ErrInvalidProtobuf, wireType)
	}
}

func decodePackedProtoInt32(data []byte, ops *int) ([]int32, error) {
	var values []int32
	offset := 0
	for offset < len(data) {
		if err := bumpCount(ops, 1, MaxSabrContextPolicyOps); err != nil {
			return nil, err
		}
		raw, n, err := readProtobufVarint(data[offset:])
		if err != nil {
			return nil, err
		}
		offset += n
		value, err := protoInt32FromVarint(raw)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func protoInt32FromVarint(value uint64) (int32, error) {
	// Match protobuf int32 decoding: keep the low 32 bits of the varint.
	return int32(value), nil
}

func bumpCount(dst *int, n, max int) error {
	if n < 0 || max < 0 {
		return ErrInvalidContextState
	}
	if n > max-*dst {
		return fmt.Errorf("%w: policy operation bound exceeded", ErrInvalidContextState)
	}
	*dst += n
	return nil
}
