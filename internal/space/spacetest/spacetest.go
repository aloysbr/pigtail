// Package spacetest holds space tables for tests. It imports nothing of
// pigtail, so internal/space's own tests can use it too.
package spacetest

// FormerYAML is a space table the tests were first written against: eight
// spaces over two scopes, a folder icon per scope, unlike the empty builtin. Parse
// it against scopetest.Three's table (or ThreeYAML): its scopes are perso
// (master) and globex.
const FormerYAML = `icons:
  globex: briefcase.fill
  perso: house.fill
spaces:
  - {name: "Garden", scope: perso}
  - {name: "Vineyard", scope: globex}
  - {name: "Meadow", scope: globex}
  - {name: "Greenhouse", scope: globex}
  - {name: "Nursery", scope: globex}
  - {name: "Allotment", scope: perso}
  - {name: "Compost", scope: perso}
  - {name: "Pond", scope: perso}
`
