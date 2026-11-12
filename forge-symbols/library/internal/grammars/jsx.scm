; Tag query for JSX in JavaScript and TSX, after the other parts. A
; component element calls its component. Lower-case names are HTML
; elements, not references.

(jsx_opening_element
  name: (identifier) @name
  (#match? @name "^[A-Z]")) @reference.call

(jsx_self_closing_element
  name: (identifier) @name
  (#match? @name "^[A-Z]")) @reference.call

[
  (jsx_opening_element
    name: (member_expression
      property: (property_identifier) @name))
  (jsx_self_closing_element
    name: (member_expression
      property: (property_identifier) @name))
] @reference.call
