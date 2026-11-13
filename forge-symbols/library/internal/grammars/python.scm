; Tag query for Python.
;
; Captures follow the Tree-sitter tags convention: @name together with
; @definition.<kind> or @reference.<kind>. Based on queries/tags.scm of
; tree-sitter-python, extended with methods, decorators, class fields,
; type aliases and superclasses. When patterns tag the same name, the first
; one wins, so the decorated patterns widen a definition to its decorators,
; and the method patterns win over the general function pattern, which
; also finds definitions the parser could not place.

; A def directly in a class body is a method.
(class_definition
  body: (block
    (decorated_definition
      definition: (function_definition
        name: (identifier) @name)) @definition.method))

(class_definition
  body: (block
    (function_definition
      name: (identifier) @name) @definition.method))

; The parser may recover a damaged class body as an error node.
(class_definition
  (ERROR
    (decorated_definition
      definition: (function_definition
        name: (identifier) @name)) @definition.method))

(class_definition
  (ERROR
    (function_definition
      name: (identifier) @name) @definition.method))

(decorated_definition
  definition: (function_definition
    name: (identifier) @name)) @definition.function

(function_definition
  name: (identifier) @name) @definition.function

(decorated_definition
  definition: (class_definition
    name: (identifier) @name)) @definition.class

(class_definition
  name: (identifier) @name) @definition.class

; Python 3.12 type aliases: type Pair = tuple[int, int].
(type_alias_statement
  left: (type
    [
      (identifier) @name
      (generic_type (identifier) @name)
    ])) @definition.type

; Assignments directly in a class body are fields.
(class_definition
  body: (block
    (expression_statement
      (assignment
        left: (identifier) @name) @definition.field)))

; Module-level assignments are constants when the name is upper case, as
; by convention, and variables otherwise.
(module
  (expression_statement
    (assignment
      left: (identifier) @name
      (#match? @name "^_*[A-Z][A-Z0-9_]*$")) @definition.constant))

(module
  (expression_statement
    (assignment
      left: (identifier) @name) @definition.variable))

(call
  function: [
    (identifier) @name
    (attribute
      attribute: (identifier) @name)
  ]) @reference.call

; A decorator without arguments, such as @property, is called with the
; definition. With arguments it is a call, matched above.
(decorator
  [
    (identifier) @name
    (attribute
      attribute: (identifier) @name)
  ]) @reference.call

(class_definition
  superclasses: (argument_list
    [
      (identifier) @name
      (attribute
        attribute: (identifier) @name)
    ] @reference.type))
