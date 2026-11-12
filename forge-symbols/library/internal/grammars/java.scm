; Tag query for Java.
;
; Captures follow the Tree-sitter tags convention: @name together with
; @definition.<kind> or @reference.<kind>. Based on queries/tags.scm of
; tree-sitter-java, extended with enums, records, annotation types,
; constructors, fields and constants. Object creation is @reference.new,
; and every other type name, including superclasses and interfaces, is
; @reference.type. When patterns tag the same name, the first one wins.

(class_declaration
  name: (identifier) @name) @definition.class

(interface_declaration
  name: (identifier) @name) @definition.interface

(enum_declaration
  name: (identifier) @name) @definition.enum

(record_declaration
  name: (identifier) @name) @definition.record

(annotation_type_declaration
  name: (identifier) @name) @definition.annotation

(method_declaration
  name: (identifier) @name) @definition.method

; Elements of an annotation type are declared like methods.
(annotation_type_element_declaration
  name: (identifier) @name) @definition.method

(constructor_declaration
  name: (identifier) @name) @definition.constructor

(compact_constructor_declaration
  name: (identifier) @name) @definition.constructor

(field_declaration
  declarator: (variable_declarator
    name: (identifier) @name)) @definition.field

; Record components are the fields of the record.
(record_declaration
  parameters: (formal_parameters
    (formal_parameter
      name: (identifier) @name) @definition.field))

; Fields of an interface are constants.
(constant_declaration
  declarator: (variable_declarator
    name: (identifier) @name)) @definition.constant

(enum_constant
  name: (identifier) @name) @definition.constant

(method_invocation
  name: (identifier) @name) @reference.call

; A method reference such as Text::trim, but not a constructor reference
; such as Text::new.
(method_reference
  "::"
  .
  (identifier) @name) @reference.call

(object_creation_expression
  type: [
    (type_identifier) @name
    (scoped_type_identifier (type_identifier) @name .)
    (generic_type (type_identifier) @name)
    (generic_type (scoped_type_identifier (type_identifier) @name .))
  ]) @reference.new

; A constructor reference such as Text::new. Without type arguments, the
; type parses as an expression.
(method_reference
  .
  [
    (identifier) @name
    (field_access field: (identifier) @name)
    (generic_type (type_identifier) @name)
    (generic_type (scoped_type_identifier (type_identifier) @name .))
  ]
  "new") @reference.new

; Annotations name their annotation type.
(marker_annotation
  name: [
    (identifier) @name
    (scoped_identifier name: (identifier) @name)
  ]) @reference.type

(annotation
  name: [
    (identifier) @name
    (scoped_identifier name: (identifier) @name)
  ]) @reference.type

(type_identifier) @name @reference.type
