; Tag query shared by JavaScript, TypeScript and TSX.
;
; Captures follow the Tree-sitter tags convention: @name together with
; @definition.<kind> or @reference.<kind>. Based on queries/tags.scm of
; tree-sitter-javascript without its doc comment predicates. Adds
; constructors, class expressions, top-level constants and variables, and
; decorators; tags object creation as @reference.new. Patterns here must
; be valid in all three grammars. When patterns tag the same name, the
; first one wins.

(method_definition
  name: (property_identifier) @name
  (#eq? @name "constructor")) @definition.constructor

; Methods of classes and object literals, getters and setters included.
(method_definition
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @name
  (#not-eq? @name "constructor")) @definition.method

[
  (class
    name: (_) @name)
  (class_declaration
    name: (_) @name)
] @definition.class

(variable_declarator
  name: (identifier) @name
  value: (class)) @definition.class

[
  (function_declaration
    name: (identifier) @name)
  (generator_function_declaration
    name: (identifier) @name)
  (function_expression
    name: (identifier) @name)
  (generator_function
    name: (identifier) @name)
] @definition.function

; Functions assigned to a name, also inside other functions.
(variable_declarator
  name: (identifier) @name
  value: [
    (arrow_function)
    (function_expression)
    (generator_function)
  ]) @definition.function

(assignment_expression
  left: [
    (identifier) @name
    (member_expression
      property: (property_identifier) @name)
  ]
  right: [
    (arrow_function)
    (function_expression)
    (generator_function)
  ]) @definition.function

(pair
  key: (property_identifier) @name
  value: [
    (arrow_function)
    (function_expression)
    (generator_function)
  ]) @definition.function

; Only top-level constants and variables are definitions.
(program
  [
    (lexical_declaration
      kind: "const"
      (variable_declarator
        name: (identifier) @name) @definition.constant)
    (export_statement
      declaration: (lexical_declaration
        kind: "const"
        (variable_declarator
          name: (identifier) @name) @definition.constant))
  ])

(program
  [
    (lexical_declaration
      kind: "let"
      (variable_declarator
        name: (identifier) @name) @definition.variable)
    (variable_declaration
      (variable_declarator
        name: (identifier) @name) @definition.variable)
    (export_statement
      declaration: [
        (lexical_declaration
          kind: "let"
          (variable_declarator
            name: (identifier) @name) @definition.variable)
        (variable_declaration
          (variable_declarator
            name: (identifier) @name) @definition.variable)
      ])
  ])

(call_expression
  function: (identifier) @name
  (#not-eq? @name "require")) @reference.call

(call_expression
  function: (member_expression
    property: [
      (property_identifier)
      (private_property_identifier)
    ] @name)) @reference.call

(new_expression
  constructor: [
    (identifier) @name
    (member_expression
      property: (property_identifier) @name)
  ]) @reference.new

; A decorator without arguments calls its function too.
(decorator
  [
    (identifier) @name
    (member_expression
      property: (property_identifier) @name)
  ]) @reference.call
