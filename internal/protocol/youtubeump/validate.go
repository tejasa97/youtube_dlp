package youtubeump

import "fmt"

func validateFormatIDWire(format FormatID) error {
	if format.Itag <= 0 {
		return fmt.Errorf("%w: missing format itag", ErrMissingConfig)
	}
	return nil
}

func validateClientInfoWire(info ClientInfo) error {
	if info.ClientName <= 0 {
		return fmt.Errorf("%w: missing client name", ErrMissingConfig)
	}
	return nil
}

func validateTimeRangeWire(tr TimeRange) error {
	if tr.StartTicks < 0 || tr.DurationTicks < 0 {
		return fmt.Errorf("%w: negative time range ticks", ErrInvalidMediaState)
	}
	if tr.Timescale < 0 {
		return ErrVarintOverflow
	}
	return nil
}

func validateBufferedRange(br BufferedRange) error {
	if err := validateFormatIDWire(br.FormatID); err != nil {
		return err
	}
	if br.StartTimeMs < 0 || br.DurationMs < 0 {
		return fmt.Errorf("%w: negative buffered range time", ErrInvalidMediaState)
	}
	if br.StartSegmentIndex < 0 || br.EndSegmentIndex < 0 {
		return ErrVarintOverflow
	}
	if _, err := int32SegmentIndex(uint64(br.StartSegmentIndex)); err != nil {
		return err
	}
	if _, err := int32SegmentIndex(uint64(br.EndSegmentIndex)); err != nil {
		return err
	}
	return validateTimeRangeWire(br.TimeRange)
}

func formatIdentityConflicts(configured, observed FormatID) error {
	if observed.Itag != 0 && observed.Itag != configured.Itag {
		return fmt.Errorf("%w: format itag mismatch", ErrInvalidMediaState)
	}
	if configured.LastModified != 0 && observed.LastModified != 0 && observed.LastModified != configured.LastModified {
		return fmt.Errorf("%w: format lastModified mismatch", ErrInvalidMediaState)
	}
	if configured.XTags != "" && observed.XTags != "" && observed.XTags != configured.XTags {
		return fmt.Errorf("%w: format xtags mismatch", ErrInvalidMediaState)
	}
	return nil
}

func headerRepresentationID(header *MediaHeader) FormatID {
	if header.FormatID.Itag != 0 || header.FormatID.LastModified != 0 || header.FormatID.XTags != "" {
		return header.FormatID
	}
	return FormatID{Itag: header.Itag}
}
