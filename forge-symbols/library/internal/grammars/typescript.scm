; Tag query for TypeScript and TSX, after ecmascript.scm. Based on
; queries/tags.scm of tree-sitter-typescript, extended with type aliases,
; enums, namespaces, fields and superclasses. Every other type name is
; @reference.type.

; Overload signatures and declared functions.
(function_signature
  name: (identifier) @name) @definition.function

; Overload signatures in classes and methods of interfaces.
(method_signature
  name: (property_identifier) @name
  (#eq? @name "constructor")) @definition.constructor

(method_signature
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @name) @definition.method

(abstract_method_signature
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @name) @definition.method

(abstract_class_declaration
  name: (type_identifier) @name) @definition.class

(interface_declaration
  name: (type_identifier) @name) @definition.interface

(type_alias_declaration
  name: (type_identifier) @name) @definition.type

(enum_declaration
  name: (identifier) @name) @definition.enum

(enum_body
  name: (property_identifier) @name @definition.constant)

(enum_assignment
  name: (property_identifier) @name) @definition.constant

[
  (module
    name: [
      (identifier)
      (nested_identifier)
    ] @name)
  (internal_module
    name: [
      (identifier)
      (nested_identifier)
    ] @name)
] @definition.module

; A field holding a class is a class, one holding a function a method.
(public_field_definition
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @name
  value: (class)) @definition.class

(public_field_definition
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @name
  value: [
    (arrow_function)
    (function_expression)
    (generator_function)
  ]) @definition.method

(public_field_definition
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @name) @definition.field

; Properties of interfaces and of object types with a name, not of inline
; types such as those of parameters.
[
  (interface_body
    (property_signature
      name: (property_identifier) @name) @definition.field)
  (type_alias_declaration
    value: (object_type
      (property_signature
        name: (property_identifier) @name) @definition.field))
]

; A superclass is an expression, not a type.
(extends_clause
  value: [
    (identifier) @name
    (member_expression
      property: (property_identifier) @name)
  ]) @reference.type

(type_identifier) @name @reference.type
