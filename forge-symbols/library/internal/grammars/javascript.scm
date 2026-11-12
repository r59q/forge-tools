; Tag query for JavaScript, after ecmascript.scm: class fields and
; superclasses, whose nodes differ in TypeScript.

; A field holding a class is a class, one holding a function a method.
(field_definition
  property: [
    (property_identifier)
    (private_property_identifier)
  ] @name
  value: (class)) @definition.class

(field_definition
  property: [
    (property_identifier)
    (private_property_identifier)
  ] @name
  value: [
    (arrow_function)
    (function_expression)
    (generator_function)
  ]) @definition.method

(field_definition
  property: [
    (property_identifier)
    (private_property_identifier)
  ] @name) @definition.field

(class_heritage
  [
    (identifier) @name
    (member_expression
      property: (property_identifier) @name)
  ]) @reference.type
