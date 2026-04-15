package grpc

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// formatMessage renders a dynamic protobuf message as human-readable text,
// similar to protobuf text format but with field names resolved from the descriptor.
func formatMessage(msg protoreflect.ProtoMessage, msgDesc protoreflect.MessageDescriptor, indent int) string {
	ref := msg.ProtoReflect()
	var sb strings.Builder
	prefix := strings.Repeat("  ", indent)

	fields := msgDesc.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !ref.Has(fd) {
			continue
		}
		val := ref.Get(fd)
		formatField(&sb, fd, val, prefix, indent)
	}
	return sb.String()
}

func formatField(sb *strings.Builder, fd protoreflect.FieldDescriptor, val protoreflect.Value, prefix string, indent int) {
	name := string(fd.Name())

	if fd.IsList() {
		list := val.List()
		for i := 0; i < list.Len(); i++ {
			formatSingleValue(sb, fd, name, list.Get(i), prefix, indent)
		}
		return
	}
	if fd.IsMap() {
		mp := val.Map()
		mp.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			sb.WriteString(fmt.Sprintf("%s%s { key: %s", prefix, name, formatScalar(fd.MapKey(), k.Value())))
			sb.WriteString(fmt.Sprintf(", value: %s }\n", formatMapValue(fd.MapValue(), v, indent+1)))
			return true
		})
		return
	}
	formatSingleValue(sb, fd, name, val, prefix, indent)
}

func formatSingleValue(sb *strings.Builder, fd protoreflect.FieldDescriptor, name string, val protoreflect.Value, prefix string, indent int) {
	if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
		nested := val.Message()
		inner := formatMessageReflect(nested, fd.Message(), indent+1)
		if strings.Contains(inner, "\n") {
			sb.WriteString(fmt.Sprintf("%s%s {\n%s%s}\n", prefix, name, inner, prefix))
		} else {
			sb.WriteString(fmt.Sprintf("%s%s { %s}\n", prefix, name, inner))
		}
		return
	}
	sb.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, name, formatScalar(fd, val)))
}

func formatMessageReflect(ref protoreflect.Message, msgDesc protoreflect.MessageDescriptor, indent int) string {
	var sb strings.Builder
	prefix := strings.Repeat("  ", indent)
	fields := msgDesc.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !ref.Has(fd) {
			continue
		}
		val := ref.Get(fd)
		formatField(&sb, fd, val, prefix, indent)
	}
	return sb.String()
}

func formatScalar(fd protoreflect.FieldDescriptor, val protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return fmt.Sprintf("%v", val.Bool())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return fmt.Sprintf("%d", val.Int())
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return fmt.Sprintf("%d", val.Int())
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return fmt.Sprintf("%d", val.Uint())
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return fmt.Sprintf("%d", val.Uint())
	case protoreflect.FloatKind:
		return fmt.Sprintf("%g", val.Float())
	case protoreflect.DoubleKind:
		return fmt.Sprintf("%g", val.Float())
	case protoreflect.StringKind:
		return fmt.Sprintf("%q", val.String())
	case protoreflect.BytesKind:
		b := val.Bytes()
		if len(b) > 64 {
			return fmt.Sprintf("<%d bytes>", len(b))
		}
		return fmt.Sprintf("%q", b)
	case protoreflect.EnumKind:
		num := val.Enum()
		enumDesc := fd.Enum().Values().ByNumber(num)
		if enumDesc != nil {
			return string(enumDesc.Name())
		}
		return fmt.Sprintf("%d", num)
	default:
		return fmt.Sprintf("%v", val.Interface())
	}
}

func formatMapValue(fd protoreflect.FieldDescriptor, val protoreflect.Value, indent int) string {
	if fd.Kind() == protoreflect.MessageKind {
		inner := formatMessageReflect(val.Message(), fd.Message(), indent)
		return fmt.Sprintf("{\n%s%s}", inner, strings.Repeat("  ", indent-1))
	}
	return formatScalar(fd, val)
}
