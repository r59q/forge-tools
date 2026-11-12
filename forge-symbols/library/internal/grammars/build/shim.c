// Shim linking the Tree-sitter runtime and one grammar into a WebAssembly
// module. It parses a source buffer, runs one tag query and returns flat
// little-endian uint32 records that the Go host decodes.
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "tree_sitter/api.h"

#ifndef SYM_LANGUAGE
#error "define SYM_LANGUAGE, e.g. -DSYM_LANGUAGE=tree_sitter_go"
#endif

#define EXPORT(name) __attribute__((export_name(#name)))
#define IMPORT(name) __attribute__((import_module("env"), import_name(#name)))
#define INIT_FAILED 0xff

const TSLanguage *SYM_LANGUAGE(void);

// should_stop asks the host whether the caller cancelled or ran out of time.
IMPORT(should_stop) uint32_t should_stop(void);

typedef struct {
  uint8_t *data;
  uint32_t length;
  uint32_t capacity;
} Buffer;

typedef struct {
  const char *data;
  uint32_t length;
} Source;

static TSParser *parser;
static TSQuery *query;
static TSQueryCursor *cursor;
static Buffer info;
static Buffer result;
static bool exhausted;

// Allocation failures trap after recording that memory ran out, so the host
// can tell the memory limit from other failures.
static void *checked(void *pointer, size_t size) {
  if (size > 0 && !pointer) {
    exhausted = true;
    __builtin_trap();
  }
  return pointer;
}

static void *checked_malloc(size_t size) {
  return checked(malloc(size), size);
}

static void *checked_calloc(size_t count, size_t size) {
  return checked(calloc(count, size), count * size);
}

static void *checked_realloc(void *pointer, size_t size) {
  return checked(realloc(pointer, size), size);
}

static void put_bytes(Buffer *buffer, const void *bytes, uint32_t length) {
  uint32_t needed = buffer->length + length;
  if (needed > buffer->capacity || !buffer->data) {
    uint32_t capacity = buffer->capacity ? buffer->capacity : 4096;
    while (capacity < needed) capacity *= 2;
    buffer->data = checked_realloc(buffer->data, capacity);
    buffer->capacity = capacity;
  }
  memcpy(buffer->data + buffer->length, bytes, length);
  buffer->length += length;
}

static void put_u32(Buffer *buffer, uint32_t value) {
  put_bytes(buffer, &value, sizeof value);
}

static void put_string(Buffer *buffer, const char *text, uint32_t length) {
  put_u32(buffer, length);
  put_bytes(buffer, text, length);
}

static uint64_t span(const Buffer *buffer) {
  return ((uint64_t)(uintptr_t)buffer->data << 32) | buffer->length;
}

static const char *read_source(void *payload, uint32_t offset, TSPoint position, uint32_t *length) {
  const Source *source = payload;
  (void)position;
  if (offset >= source->length) {
    *length = 0;
    return "";
  }
  *length = source->length - offset;
  return source->data + offset;
}

static bool parse_stopped(TSParseState *state) {
  (void)state;
  return should_stop() != 0;
}

static bool query_stopped(TSQueryCursorState *state) {
  (void)state;
  return should_stop() != 0;
}

static TSTree *parse(const char *data, uint32_t length) {
  Source source = {data, length};
  TSInput input = {&source, read_source, TSInputEncodingUTF8, NULL};
  TSParseOptions options = {NULL, parse_stopped};

  TSTree *tree = ts_parser_parse_with_options(parser, NULL, input, options);
  if (!tree) ts_parser_reset(parser);
  return tree;
}

static void put_match(const TSQueryMatch *match) {
  put_u32(&result, match->pattern_index);
  put_u32(&result, match->capture_count);
  for (uint16_t i = 0; i < match->capture_count; i++) {
    put_u32(&result, match->captures[i].index);
    put_u32(&result, ts_node_start_byte(match->captures[i].node));
    put_u32(&result, ts_node_end_byte(match->captures[i].node));
  }
}

// sym_alloc reserves guest memory for the host to copy input into. It
// returns 0 when memory runs out.
EXPORT(sym_alloc) void *sym_alloc(uint32_t size) {
  return malloc(size ? size : 1);
}

// sym_free releases memory from sym_alloc.
EXPORT(sym_free) void sym_free(void *pointer) {
  free(pointer);
}

// sym_exhausted reports whether an allocation failed since the module started.
EXPORT(sym_exhausted) uint32_t sym_exhausted(void) {
  return exhausted;
}

// sym_init compiles the tag query. It returns 0, or the query error type in
// the high word and the error offset in the low word.
EXPORT(sym_init) uint64_t sym_init(const char *source, uint32_t length) {
  uint32_t offset = 0;
  TSQueryError error = TSQueryErrorNone;

  ts_set_allocator(checked_malloc, checked_calloc, checked_realloc, free);
  parser = ts_parser_new();
  if (!ts_parser_set_language(parser, SYM_LANGUAGE())) return (uint64_t)INIT_FAILED << 32;

  query = ts_query_new(SYM_LANGUAGE(), source, length, &offset, &error);
  if (!query) return ((uint64_t)error << 32) | offset;

  cursor = ts_query_cursor_new();
  return 0;
}

// sym_info describes the query: capture names, predicate strings and the
// predicate steps of each pattern. Strings are length-prefixed.
EXPORT(sym_info) uint64_t sym_info(void) {
  uint32_t length = 0;
  uint32_t captures = ts_query_capture_count(query);
  uint32_t strings = ts_query_string_count(query);
  uint32_t patterns = ts_query_pattern_count(query);

  info.length = 0;
  put_u32(&info, captures);
  for (uint32_t i = 0; i < captures; i++) {
    const char *name = ts_query_capture_name_for_id(query, i, &length);
    put_string(&info, name, length);
  }

  put_u32(&info, strings);
  for (uint32_t i = 0; i < strings; i++) {
    const char *value = ts_query_string_value_for_id(query, i, &length);
    put_string(&info, value, length);
  }

  put_u32(&info, patterns);
  for (uint32_t i = 0; i < patterns; i++) {
    const TSQueryPredicateStep *steps = ts_query_predicates_for_pattern(query, i, &length);
    put_u32(&info, length);
    for (uint32_t j = 0; j < length; j++) {
      put_u32(&info, steps[j].type);
      put_u32(&info, steps[j].value_id);
    }
  }
  return span(&info);
}

// sym_analyze parses source and runs the tag query. The result holds, per
// match, the pattern index, the capture count and for each capture its index,
// start byte and end byte. It returns 0 when the host stopped the parse.
EXPORT(sym_analyze) uint64_t sym_analyze(const char *source, uint32_t length) {
  TSQueryMatch match;
  TSQueryCursorOptions options = {NULL, query_stopped};

  TSTree *tree = parse(source, length);
  if (!tree) return 0;

  result.length = 0;
  put_bytes(&result, "", 0);
  ts_query_cursor_exec_with_options(cursor, query, ts_tree_root_node(tree), &options);
  while (ts_query_cursor_next_match(cursor, &match)) put_match(&match);

  ts_tree_delete(tree);
  return span(&result);
}
