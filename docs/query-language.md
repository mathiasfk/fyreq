# Query language

The initial language is a deliberately small SQL-like subset:

```text
SELECT (* | field [, field ...])
FROM files
[WHERE field operator literal [AND field operator literal ...]]
[LIMIT non_negative_integer]
[;]
```

Keywords and field names are case-insensitive. Strings use single quotes;
represent a quote inside a string by doubling it: `'O''Brien'`. Strings are
compared exactly and case-sensitively, independently of filesystem case rules.
Backslashes are literal characters rather than escape sequences.

## Fields

| Field | Type | Meaning |
| --- | --- | --- |
| `id` | String | Internal path-record identifier |
| `path` | String | Absolute lexical path |
| `name` | String | Basename |
| `kind` | String | `file`, `directory`, `symlink`, or `other` |
| `size` | Integer | OS-reported size in bytes, not recursive directory usage |
| `modified_at` | Timestamp | Observed last modification time |
| `indexed_at` | Timestamp | Time this record was observed |

`SELECT *` expands to the fields in the order above. Native filesystem identity
is stored internally and is not a query field yet.

## Comparisons

Supported operators are `=`, `!=`, `<`, `<=`, `>`, and `>=`. String ordering is
lexical. Size literals are unquoted non-negative signed 64-bit integers.
Timestamp literals must be single-quoted RFC 3339 values with a timezone; they
are compared as instants, including fractional seconds when present.

```sql
SELECT path, size FROM files WHERE kind = 'file' AND size >= 1048576 LIMIT 20
SELECT path FROM files WHERE name = 'O''Brien'
SELECT path, modified_at FROM files WHERE modified_at >= '2026-01-01T00:00:00Z'
SELECT path FROM files WHERE path = '/Users/example/Projects/app/main.go'
```

Results are ordered by absolute path before applying `LIMIT`. `LIMIT 0` produces
an empty result with its projected columns. This deterministic ordering is the
current execution behavior; explicit `ORDER BY` is not supported.

Unknown fields, unknown relations, invalid literals, unsupported operators, and
trailing statements produce errors. There is no `OR`, `LIKE`, glob matching,
path-descendant operator, arithmetic, aggregation, joins, aliases, or live
filesystem access yet.

## Results and coverage

JSON query output has this shape:

```json
{
  "columns": ["path", "size"],
  "rows": [["/Users/example/Projects/app/main.go", 1234]],
  "generation": 1,
  "coverage": {}
}
```

The empty coverage map here illustrates the shape only. A collected catalog
includes coverage objects keyed by requested root, with observation timestamps,
completeness, and diagnostics. JSON timestamps use RFC 3339. Empty rows are `[]`.
Text output renders a table and directs users to `status` for coverage details.

All results reflect the selected persisted generation. The query engine does not
infer freshness or completeness from the query predicate. A missing record can
mean absence, lack of coverage, or a change since collection.
