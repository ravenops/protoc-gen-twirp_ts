package main

import (
	"fmt"
	"log"
	"path"
	"strings"

	"github.com/golang/protobuf/protoc-gen-go/descriptor"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
)

func sameFile(a *descriptor.FileDescriptorProto, b *descriptor.FileDescriptorProto) bool {
	return a.GetName() == b.GetName()
}

func fullTypeName(fd *descriptor.FileDescriptorProto, typeName string) string {
	return fmt.Sprintf(".%s.%s", fd.GetPackage(), typeName)
}

func generate(req *plugin.CodeGeneratorRequest) (*plugin.CodeGeneratorResponse, error) {
	resolver := dependencyResolver{}

	res := &plugin.CodeGeneratorResponse{
		File: []*plugin.CodeGeneratorResponse_File{
			{
				Name:    &twirpFileName,
				Content: &twirpSource,
			},
		},
	}

	outputFiles := make(map[string][]*protoFile)
	protoFiles := req.GetProtoFile()
	for _, file := range protoFiles {
		pfile := &protoFile{
			Output:             tsFileName(file),
			RelativeImportBase: relativeImportBase(file),
			Imports:            map[string]*importValues{},
			Messages:           []*messageValues{},
			Services:           []*serviceValues{},
			Enums:              []*enumValues{},
		}
		outputFiles[tsImportPath(file)] = append(outputFiles[tsImportPath(file)], pfile)

		// Add enum
		for _, enum := range file.GetEnumType() {
			resolver.Set(file, enum.GetName())

			v := &enumValues{
				Name:   enum.GetName(),
				Values: []*enumKeyVal{},
			}

			for _, value := range enum.GetValue() {
				v.Values = append(v.Values, &enumKeyVal{
					Name:  value.GetName(),
					Value: value.GetNumber(),
				})
			}

			pfile.Enums = append(pfile.Enums, v)
		}

		// Add messages
		for _, message := range file.GetMessageType() {
			name := message.GetName()
			tsInterface := typeToInterface(name)
			jsonInterface := typeToJSONInterface(name)

			resolver.Set(file, name)
			resolver.Set(file, tsInterface)
			resolver.Set(file, jsonInterface)

			v := &messageValues{
				Name:          name,
				Interface:     tsInterface,
				JSONInterface: jsonInterface,

				Fields:      []*fieldValues{},
				NestedTypes: []*messageValues{},
				NestedEnums: []*enumValues{},
			}

			if len(message.GetNestedType()) > 0 {
				// TODO: add support for nested messages
				// https://developers.google.com/protocol-buffers/docs/proto#nested
				log.Printf("warning: nested messages are not supported yet")
			}

			// Add nested enums
			for _, enum := range message.GetEnumType() {
				e := &enumValues{
					Name:   fmt.Sprintf("%s_%s", message.GetName(), enum.GetName()),
					Values: []*enumKeyVal{},
				}

				for _, value := range enum.GetValue() {
					e.Values = append(e.Values, &enumKeyVal{
						Name:  value.GetName(),
						Value: value.GetNumber(),
					})
				}

				v.NestedEnums = append(v.NestedEnums, e)
			}

			// Add message fields
			for _, field := range message.GetField() {
				typeName := resolver.TypeName(file, singularFieldType(message, field))
				fp, err := resolver.Resolve(field.GetTypeName())
				if err == nil {
					if !sameFile(fp, file) {
						pfile.AddImport(fp, typeName)
					}
				}

				v.Fields = append(v.Fields, &fieldValues{
					Name:  field.GetName(),
					Field: camelCase(field.GetName()),

					Type:       typeName,
					IsEnum:     field.GetType() == descriptor.FieldDescriptorProto_TYPE_ENUM,
					IsRepeated: isRepeated(field),
				})
			}

			pfile.Messages = append(pfile.Messages, v)
		}

		// Add services
		for _, service := range file.GetService() {
			resolver.Set(file, service.GetName())

			v := &serviceValues{
				Package:   file.GetPackage(),
				Name:      service.GetName(),
				Interface: typeToInterface(service.GetName()),
				Methods:   []*serviceMethodValues{},
			}

			for _, method := range service.GetMethod() {
				inputType := resolver.TypeName(file, removePkg(method.GetInputType()))
				outputType := resolver.TypeName(file, removePkg(method.GetOutputType()))
				{
					fp, err := resolver.Resolve(method.GetInputType())
					if err == nil {
						if !sameFile(fp, file) {
							pfile.AddImport(fp, inputType)
						}
					}
				}

				{
					fp, err := resolver.Resolve(method.GetOutputType())
					if err == nil {
						if !sameFile(fp, file) {
							pfile.AddImport(fp, outputType)
						}
					}
				}

				v.Methods = append(v.Methods, &serviceMethodValues{
					Name:       method.GetName(),
					InputType:  inputType,
					OutputType: outputType,
				})
			}

			pfile.Services = append(pfile.Services, v)
		}
	}

	for tsPath, pff := range outputFiles {
		ev := &exportValues{}

		for _, pf := range pff {
			ev.Exports = append(ev.Exports, strings.TrimSuffix(path.Base(pf.Output), ".ts"))

			// Compile to typescript
			content, err := pf.Compile()
			if err != nil {
				log.Fatal("could not compile template: ", err)
			}

			// Add to file list
			res.File = append(res.File, &plugin.CodeGeneratorResponse_File{
				Name:    &pf.Output,
				Content: &content,
			})
		}

		content, err := ev.Compile()
		if err != nil {
			log.Fatal("could not compile template: ", err)
		}

		name := path.Join(tsPath, "index.ts")
		res.File = append(res.File, &plugin.CodeGeneratorResponse_File{
			Name:    &name,
			Content: &content,
		})
	}

	for i := range res.File {
		log.Printf("wrote: %v", *res.File[i].Name)
	}

	return res, nil
}

func isRepeated(field *descriptor.FieldDescriptorProto) bool {
	return field.Label != nil && *field.Label == descriptor.FieldDescriptorProto_LABEL_REPEATED
}

func removePkg(s string) string {
	p := strings.SplitN(s, ".", 3)
	c := strings.Split(p[len(p)-1], ".")
	return strings.Join(c, "_")
}

func upperCaseFirst(s string) string {
	return strings.ToUpper(s[0:1]) + s[1:]
}

func camelCase(s string) string {
	parts := strings.Split(s, "_")

	for i, p := range parts {
		if i == 0 {
			parts[i] = p
		} else {
			parts[i] = strings.ToUpper(p[0:1]) + p[1:]
		}
	}

	return strings.Join(parts, "")
}

func importName(fp *descriptor.FileDescriptorProto) string {
	return tsImportName(fp.GetPackage())
}

func tsImportName(name string) string {
	base := path.Base(name)
	return base[0 : len(base)-len(path.Ext(base))]
}

func tsImportPath(fd *descriptor.FileDescriptorProto) string {
	return path.Join(strings.Split(fd.GetPackage(), ".")...)
}

func relativeImportBase(fd *descriptor.FileDescriptorProto) string {
	return strings.Repeat("../", len(strings.Split(tsImportPath(fd), "/")))
}

func tsFileName(fd *descriptor.FileDescriptorProto) string {
	filename := strings.TrimSuffix(path.Base(fd.GetName()), path.Ext(fd.GetName())) + ".ts"
	return path.Join(tsImportPath(fd), filename)
}

func singularFieldType(m *descriptor.DescriptorProto, f *descriptor.FieldDescriptorProto) string {
	switch f.GetType() {
	case descriptor.FieldDescriptorProto_TYPE_DOUBLE,
		descriptor.FieldDescriptorProto_TYPE_FIXED32,
		descriptor.FieldDescriptorProto_TYPE_FIXED64,
		descriptor.FieldDescriptorProto_TYPE_INT32,
		descriptor.FieldDescriptorProto_TYPE_INT64,
		descriptor.FieldDescriptorProto_TYPE_UINT32,
		descriptor.FieldDescriptorProto_TYPE_UINT64:
		return "number"
	case descriptor.FieldDescriptorProto_TYPE_ENUM:
		return removePkg(f.GetTypeName())
	case descriptor.FieldDescriptorProto_TYPE_STRING:
		return "string"
	case descriptor.FieldDescriptorProto_TYPE_BOOL:
		return "boolean"
	case descriptor.FieldDescriptorProto_TYPE_MESSAGE:
		name := f.GetTypeName()

		// Google WKT Timestamp is a special case here:
		//
		// Currently the value will just be left as jsonpb RFC 3339 string.
		// JSON.stringify already handles serializing Date to its RFC 3339 format.
		//
		if name == ".google.protobuf.Timestamp" {
			return "Date"
		}

		return removePkg(name)
	default:
		//log.Printf("unknown type %q in field %q", f.GetType(), f.GetName())
		return "string"
	}

}

func fieldType(f *fieldValues) string {
	t := f.Type
	if t == "Date" {
		t = "string"
	}
	if f.IsRepeated {
		return t + "[]"
	}
	return t
}
