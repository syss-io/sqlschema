BUF_VERSION := v1.47.2
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

PROTO_DIR := protobuf

default: buf

# Generate code from proto files
.PHONY: buf-generate
buf-generate: 
	    @echo "Generating code from proto files..."
	    $(BUF) dep update
	    $(BUF) generate

# Clean generated files (customize patterns as needed)
.PHONY: clean
buf-clean:
	    @echo "Cleaning generated files..."
	    find . -name "*.pb.go" -delete
	    find . -name "*.pb.gw.go" -delete

# Main buf workflow
.PHONY: buf
buf: buf-clean buf-generate
