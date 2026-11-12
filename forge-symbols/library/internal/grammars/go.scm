; Tag query for Go.
;
; Captures follow the Tree-sitter tags convention: @name together with
; @definition.<kind> or @reference.<kind>. @container names the type a
; definition belongs to when it is not nested inside it.

(function_declaration
  name: (identifier) @name
  (#not-eq? @name "_")) @definition.function

; The receiver type is the container. Other receivers still give a method.
(method_declaration
  receiver: (parameter_list
    (parameter_declaration
      type: [
        (type_identifier) @container
        (generic_type type: (type_identifier) @container)
        (pointer_type (type_identifier) @container)
        (pointer_type (generic_type type: (type_identifier) @container))
      ]))?
  name: (field_identifier) @name) @definition.method

; Methods of an interface.
(method_elem
  name: (field_identifier) @name) @definition.method

(type_spec
  name: (type_identifier) @name
  type: (struct_type)) @definition.struct

(type_spec
  name: (type_identifier) @name
  type: (interface_type)) @definition.interface

(type_spec
  name: (type_identifier) @name
  type: [
    (type_identifier)
    (qualified_type)
    (generic_type)
    (pointer_type)
    (array_type)
    (slice_type)
    (map_type)
    (channel_type)
    (function_type)
    (parenthesized_type)
  ]) @definition.type

(type_alias
  name: (type_identifier) @name) @definition.type

(field_declaration
  name: (field_identifier) @name
  (#not-eq? @name "_")) @definition.field

; Only package-level constants and variables are definitions. The grammar
; marks a whole list of constant names as one field, which a field pattern
; matches only once, so constant names are matched as plain children.
(source_file
  (const_declaration
    (const_spec
      (identifier) @name) @definition.constant)
  (#not-eq? @name "_"))

(source_file
  (var_declaration [
    (var_spec
      name: (identifier) @name) @definition.variable
    (var_spec_list
      (var_spec
        name: (identifier) @name) @definition.variable)
  ])
  (#not-eq? @name "_"))

(call_expression
  function: [
    (identifier) @name
    (parenthesized_expression (identifier) @name)
    (selector_expression field: (field_identifier) @name)
    (parenthesized_expression (selector_expression field: (field_identifier) @name))
  ]) @reference.call

(type_identifier) @name @reference.type
