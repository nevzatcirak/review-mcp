// Package data holds the fixed tables of internal/related.
package data

// The stop list of the changed contract surface (v2.5 spec V-13b §2.1 item
// 5): tokens of a contract file's changed lines that name the description
// language, a type or a common word rather than something a consumer uses.
// A token is dropped when its lower-case form is listed. It is
// review-mcp's own; nothing here is adapted from PR-Agent.

// SchemaKeywords are the keywords of API description languages: OpenAPI
// and JSON Schema, protobuf, GraphQL, Avro, Thrift and WSDL (with XML
// Schema), lower case.
var SchemaKeywords = []string{
	// OpenAPI, Swagger, AsyncAPI and JSON Schema.
	"openapi", "swagger", "asyncapi", "info", "title", "version", "paths", "components", "schemas", "schema",
	"properties", "property", "required", "type", "format", "items", "enum", "ref", "allof", "anyof", "oneof",
	"not", "nullable", "default", "deprecated", "requestbody", "responses", "response", "content", "parameters",
	"parameter", "operationid", "servers", "server", "url", "tags", "tag", "security", "securityschemes",
	"definitions", "consumes", "produces", "basepath", "host", "schemes", "discriminator", "propertyname",
	"mapping", "const", "additionalproperties", "minimum", "maximum", "exclusiveminimum", "exclusivemaximum",
	"multipleof", "minlength", "maxlength", "pattern", "minitems", "maxitems", "uniqueitems", "minproperties",
	"maxproperties", "readonly", "writeonly", "externaldocs", "license", "contact", "terms", "termsofservice",
	"get", "put", "post", "delete", "patch", "head", "options", "trace", "path", "query", "header", "headers",
	"cookie", "style", "explode", "in", "application", "json", "xml", "yaml", "text", "plain", "http", "https",
	"bearer", "oauth2", "apikey", "flows", "scopes", "callbacks", "links", "channels", "subscribe", "publish",
	"payload", "draft", "contentencoding", "contentmediatype", "dependencies", "dependentrequired", "defs",
	"definitions", "anchor", "comment", "examples",
	// protobuf.
	"syntax", "proto2", "proto3", "package", "import", "option", "message", "service", "rpc", "returns", "stream",
	"repeated", "optional", "reserved", "map", "extend", "extensions", "public", "weak", "max", "edition",
	"json_name", "packed", "go_package", "java_package", "java_multiple_files", "java_outer_classname",
	"csharp_namespace", "objc_class_prefix", "php_namespace", "ruby_package", "swift_prefix", "google",
	"protobuf", "empty", "wrappers", "field_mask", "fieldmask",
	// GraphQL.
	"input", "mutation", "subscription", "interface", "union", "scalar", "implements", "directive", "fragment",
	"repeatable", "on",
	// Avro.
	"namespace", "name", "fields", "record", "symbols", "values", "aliases", "doc", "order", "fixed", "size",
	"logicaltype", "precision", "scale", "protocol", "messages", "request", "errors", "ascending", "descending",
	"ignore",
	// Thrift.
	"include", "cpp_include", "typedef", "struct", "exception", "extends", "oneway", "throws", "list", "set",
	"senum", "slist",
	// WSDL and XML Schema.
	"wsdl", "xsd", "xsi", "xmlns", "soap", "soap12", "soapaction", "targetnamespace", "porttype", "operation",
	"output", "fault", "binding", "port", "element", "complextype", "simpletype", "complexcontent",
	"simplecontent", "sequence", "all", "choice", "attribute", "attributegroup", "group", "minoccurs",
	"maxoccurs", "restriction", "extension", "base", "annotation", "documentation", "literal", "encoded", "use",
	"location", "address", "part", "parts", "transport", "document", "nillable", "elementformdefault",
	"attributeformdefault", "qualified", "unqualified", "unbounded",
}

// TypeNames are the type names of API descriptions and common languages,
// lower case.
var TypeNames = []string{
	"string", "strings", "integer", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32",
	"uint64", "sint32", "sint64", "fixed32", "fixed64", "sfixed32", "sfixed64", "i16", "i32", "i64", "float",
	"float32", "float64", "double", "decimal", "number", "numeric", "boolean", "bool", "object", "array",
	"bytes", "byte", "binary", "base64", "char", "long", "short", "void", "any", "unknown", "never", "null",
	"date", "time", "datetime", "timestamp", "duration", "uuid", "uri", "email", "hostname", "ipv4", "ipv6",
	"password", "byte", "id", "char", "dict", "tuple", "optional", "nullable", "record", "list",
}

// CommonWords are words of descriptions and comments, and literal values,
// lower case.
var CommonWords = []string{
	"the", "and", "for", "with", "from", "that", "this", "these", "those", "are", "was", "were", "not", "but",
	"can", "will", "has", "have", "had", "all", "its", "into", "when", "which", "who", "what", "where", "how",
	"use", "used", "uses", "using", "may", "must", "should", "shall", "also", "only", "each", "per", "more",
	"less", "than", "then", "there", "their", "they", "them", "you", "your", "our", "out", "off", "via", "see",
	"new", "old", "now", "one", "two", "three", "first", "last", "other", "some", "such", "does", "did", "done",
	"been", "being", "any", "set", "get", "returned", "return", "returns", "value", "values", "key", "keys",
	"description", "example", "examples", "summary", "note", "notes", "todo", "fixme", "true", "false", "null",
	"nil", "none", "yes", "data", "result", "results", "item", "list", "page", "size", "total", "count",
	"field", "fields", "object", "objects", "array", "string", "number", "default", "optional", "required",
	"deprecated", "www", "com", "org", "net", "example",
}
