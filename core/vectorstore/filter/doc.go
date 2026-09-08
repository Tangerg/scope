// Package filter defines the stable metadata-filter expression vocabulary used
// by vector stores.
//
// Callers can build predicates with typed constructors such as [EQ], [GE],
// [In], [Has], [And], and [Not], or parse the textual DSL with [Parse]. [In]
// tests a selected scalar against a supplied list; [Has] tests
// whether a selected collection contains one complete scalar element.
// The returned [Predicate] tree contains only semantic nodes ([Ident], [Literal],
// [ListLiteral], [UnaryExpr], [BinaryExpr], and [IndexExpr]); lexer tokens,
// scanner state, and parser state are unexported implementation details.
//
// Example:
//
//	expr := filter.And(
//		filter.EQ("category", "tech"),
//		filter.GE("year", 2020),
//	)
//	if err := expr.Validate(); err != nil {
//		return err
//	}
//
// [Parse] validates and normalizes the tree before a provider translates it.
// Provider compilers and interpreters can share the complete-tree [Visitor]
// contract through [Predicate.Accept]. Selectors, literals, lists, and operator
// nodes own their validation, conversion, dispatch, and formatting behavior.
//
// [Match] is the one client-side evaluation of a predicate. A store whose
// provider cannot express a filter — or cannot enumerate matches exhaustively —
// evaluates it here instead of reimplementing the semantics, so provider-side
// translation and local evaluation cannot drift apart.
package filter
