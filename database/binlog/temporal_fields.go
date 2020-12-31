package binlog

import (
	"fmt"
	"time"

	"github.com/dropbox/godropbox/errors"
	mysql_proto "github.com/dropbox/godropbox/proto/mysql"
)

// This contains field descriptors for temporal types as defined by
// sql/field.h.  In particular:
//
// Field (abstract)
// |
// ...
// |     +--Field_year
// ...
// |
// +--Field_temporal (abstract)
//      +--Field_time_common (abstract)
//    |  +--Field_time
//    |  +--Field_timef
//    |
//    +--Field_temporal_with_date (abstract)
//       +--Field_newdate
//       +--Field_temporal_with_date_and_time (abstract)
//          +--Field_timestamp
//          +--Field_datetime
//          +--Field_temporal_with_date_and_timef (abstract)
//             +--Field_timestampf
//             +--Field_datetimef

// This returns a field descriptor for FieldType_YEAR (i.e., Field_year)
func NewYearFieldDescriptor(nullable NullableColumn) FieldDescriptor {
	return newFixedLengthFieldDescriptor(
		mysql_proto.FieldType_YEAR,
		nullable,
		1,
		func(b []byte) interface{} {
			return time.Date(int(b[0])+1900, 0, 0, 0, 0, 0, 0, time.UTC)
		})
}

// This returns a fields descriptor for FieldType_TIMESTAMP
// (i.e., Field_timestamp)
func NewTimestampFieldDescriptor(nullable NullableColumn) FieldDescriptor {
	return newFixedLengthFieldDescriptor(
		mysql_proto.FieldType_TIMESTAMP,
		nullable,
		4,
		func(b []byte) interface{} {
			return time.Unix(int64(LittleEndian.Uint32(b)), 0).UTC()
		})
}

// This returns a fields descriptor for FieldType_DATETIME
// (i.e., Field_datetime).  See number_to_datetime (in sql-common/my_time.c)
// for encoding detail.
func NewDateTimeFieldDescriptor(nullable NullableColumn) FieldDescriptor {
	return newFixedLengthFieldDescriptor(
		mysql_proto.FieldType_DATETIME,
		nullable,
		8,
		func(b []byte) interface{} {
			val := LittleEndian.Uint64(b)
			d := val / 1000000
			t := val % 1000000
			return time.Date(
				int(d/10000),              // year
				time.Month((d%10000)/100), // month
				int(d%100),                // day
				int(t/10000),              // hour
				int((t%10000)/100),        // minute
				int(t%100),                // second
				0,                         // nanosecond
				time.UTC)
		})
}

func NewDateFieldDescriptor(nullable NullableColumn) FieldDescriptor {
	return newFixedLengthFieldDescriptor(
		mysql_proto.FieldType_DATE,
		nullable,
		3,
		func(buf []byte) interface{} {
			// See https://dev.mysql.com/doc/internals/en/date-and-time-data-type-representation.html
			i32 := uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16
			if i32 == 0 {
				return "0000-00-00"
			}
			return fmt.Sprintf("%04d-%02d-%02d", i32/(16*32), i32/32%16, i32%32)
		},
	)
}

func NewTimeFieldDescriptor(nullable NullableColumn) FieldDescriptor {
	return newFixedLengthFieldDescriptor(
		mysql_proto.FieldType_TIME,
		nullable,
		3,
		func(buf []byte) interface{} {
			// See https://dev.mysql.com/doc/internals/en/date-and-time-data-type-representation.html
			i32 := uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16
			if i32 == 0 {
				return "00:00:00"
			}
			return fmt.Sprintf("%02d:%02d:%02d", i32/10000, (i32%10000)/100, i32%100)
		},
	)
}

// Common functionality for datetime2 and timestamp2
type usecTemporalFieldDescriptor struct {
	baseFieldDescriptor

	microSecondPrecision uint8
	fixedSize            int
	neededBytes          int
}

func (d *usecTemporalFieldDescriptor) init(
	fieldType mysql_proto.FieldType_Type,
	nullable NullableColumn,
	fixedSize int,
	metadata []byte) (
	remaining []byte,
	err error) {

	d.fieldType = fieldType
	d.isNullable = nullable

	if len(metadata) < 1 {
		return nil, errors.New("Metadata has too few bytes")
	}

	d.fixedSize = fixedSize
	d.neededBytes = fixedSize
	d.microSecondPrecision = uint8(metadata[0])
	switch d.microSecondPrecision {
	case 0:
		// do nothing
	case 1, 2:
		d.neededBytes++
	case 3, 4:
		d.neededBytes += 2
	case 5, 6:
		d.neededBytes += 3
	default:
		return nil, errors.New("Invalid usec precision")
	}

	return metadata[1:], nil
}

func (d *usecTemporalFieldDescriptor) readData(data []byte) (
	fixedBytes []byte,
	msec int64,
	remaining []byte,
	err error) {

	raw, remaining, err := readSlice(data, d.neededBytes)
	if err != nil {
		return nil, 0, nil, err
	}

	msecBytes := raw[d.fixedSize:]

	msec = int64(0)
	switch d.microSecondPrecision {
	case 0:
		// do nothing
	case 1, 2:
		msec = int64(BigEndian.Int8(msecBytes)) * 10000
	case 3, 4:
		msec = int64(BigEndian.Int16(msecBytes)) * 100
	case 5, 6:
		msec = int64(BigEndian.Int24(msecBytes))
	}

	return raw[:d.fixedSize], msec, remaining, nil
}

type time2FieldDescriptor struct {
	usecTemporalFieldDescriptor
}

func NewTime2FieldDescriptor(nullable NullableColumn, metadata []byte) (
	fd FieldDescriptor,
	remaining []byte,
	err error) {
	t := &time2FieldDescriptor{}
	remaining, err = t.init(
		mysql_proto.FieldType_TIME2,
		nullable,
		3,
		metadata)

	if err != nil {
		return nil, nil, err
	}

	return t, remaining, nil
}

type timestamp2FieldDescriptor struct {
	usecTemporalFieldDescriptor
}

func (d *time2FieldDescriptor) ParseValue(data []byte) (
	value interface{},
	remaining []byte,
	err error) {

	secBytes, msec, remaining, err := d.readData(data)
	if err != nil {
		return nil, nil, err
	}
	// See https://dev.mysql.com/doc/internals/en/date-and-time-data-type-representation.html
	hms := uint64(secBytes[2]) | uint64(secBytes[1])<<8 | uint64(secBytes[0])<<16
	hour := (hms >> 12) % (1 << 10) /* 10 bits starting at 12th */
	minute := (hms >> 6) % (1 << 6) /* 6 bits starting at 6th   */
	second := hms % (1 << 6)        /* 6 bits starting at 0th   */
	return time.Date(
		0,
		0,
		0,
		int(hour),
		int(minute),
		int(second),
		int(msec)*1000, // nanosecond
		time.UTC), remaining, nil
}

// This returns a field descriptor for FieldType_TIMESTAMP2
// (i.e., Field_timestampf).  See my_timestamp_from_binary (in
// sql-common/my_time.c) for encoding detail.
func NewTimestamp2FieldDescriptor(nullable NullableColumn, metadata []byte) (
	fd FieldDescriptor,
	remaining []byte,
	err error) {

	t := &timestamp2FieldDescriptor{}
	remaining, err = t.init(
		mysql_proto.FieldType_TIMESTAMP2,
		nullable,
		4,
		metadata)

	if err != nil {
		return nil, nil, err
	}

	return t, remaining, nil
}

func (d *timestamp2FieldDescriptor) ParseValue(data []byte) (
	value interface{},
	remaining []byte,
	err error) {

	secBytes, msec, remaining, err := d.readData(data)
	if err != nil {
		return nil, nil, err
	}

	sec := int64(BigEndian.Int32(secBytes))

	return time.Unix(sec, msec*1000).UTC(), remaining, nil
}

// equivalent to DATETIMEF_INT_OFS
const datetimefIntOffset = 0x8000000000

type datetime2FieldDescriptor struct {
	usecTemporalFieldDescriptor
}

// This returns a field descriptor for FieldType_DATETIME2
// (i.e., Field_datetimef).  See TIME_from_longlong_datetime_packed (
// in sql-common/my_time.c) for encoding detail.
func NewDateTime2FieldDescriptor(nullable NullableColumn, metadata []byte) (
	fd FieldDescriptor,
	remaining []byte,
	err error) {

	d := &datetime2FieldDescriptor{}

	remaining, err = d.init(
		mysql_proto.FieldType_DATETIME2,
		nullable,
		5,
		metadata)

	if err != nil {
		return nil, nil, err
	}

	return d, remaining, nil
}

func (d *datetime2FieldDescriptor) ParseValue(data []byte) (
	value interface{},
	remaining []byte,
	err error) {

	dtBytes, msec, remaining, err := d.readData(data)
	if err != nil {
		return nil, nil, err
	}

	ymdhms := BigEndian.Uint40(dtBytes) - datetimefIntOffset

	ymd := ymdhms >> 17
	ym := ymd >> 5
	hms := ymdhms % (1 << 17)

	day := ymd % (1 << 5)
	month := ym % 13
	year := ym / 13

	second := hms % (1 << 6)
	minute := (hms >> 6) % (1 << 6)
	hour := hms >> 12

	return time.Date(
		int(year),
		time.Month(month),
		int(day),
		int(hour),
		int(minute),
		int(second),
		int(msec)*1000, // nanosecond
		time.UTC), remaining, nil
}
