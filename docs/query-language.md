# Query language

The initial language is a deliberately small SQL-like subset:

```text
SELECT (* | field [, field ...])
FROM files
[WHERE field operator literal [AND field operator literal ...]]
[ORDER BY field [ASC | DESC] [, field [ASC | DESC] ...]]
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
| `uid` | Nullable integer | Numeric filesystem owner ID, collected on macOS |
| `owner` | Nullable string | Owner account name resolved at collection time |

`SELECT *` expands to the fields in the order above. Native filesystem identity
is stored internally and is not a query field yet.

Ownership means the filesystem owner, not the document author or original
creator. The account name is resolved once per UID per scan and can be unavailable
even when the numeric UID is known. Ownership of symlinks describes the link.
Older catalogs remain readable: records without these optional fields return
`null` until refreshed. Unsupported platforms also leave ownership unavailable.
Unavailable values appear as `null` in JSON and `NULL` in text output. They never
satisfy a comparison, including `!=`. `IS NULL` is not supported yet.

## Comparisons

Supported operators are `=`, `!=`, `<`, `<=`, `>`, and `>=`. String ordering is
lexical. Size and UID literals are unquoted non-negative signed 64-bit integers.
Timestamp literals must be single-quoted RFC 3339 values with a timezone; they
are compared as instants, including fractional seconds when present.

```sql
SELECT path, size FROM files WHERE kind = 'file' AND size >= 1048576 LIMIT 20
SELECT path FROM files WHERE name = 'O''Brien'
SELECT path, modified_at FROM files WHERE modified_at >= '2026-01-01T00:00:00Z'
SELECT path FROM files WHERE path = '/Users/example/Projects/app/main.go'
SELECT path, uid, owner FROM files WHERE owner = 'root'
SELECT path FROM files WHERE uid = 0
```

## Ordering and limits

`ORDER BY` accepts any query field, including fields omitted from `SELECT`.
Each field defaults to `ASC` (ascending); use `DESC` for descending order. Multiple
fields are evaluated left to right. Strings sort lexically and case-sensitively,
integers sort numerically, and timestamps sort by instant. Unavailable values
always sort last in both directions. `NULLS FIRST` and `NULLS LAST` syntax is not
supported.

```sql
SELECT path, size FROM files WHERE kind = 'file' ORDER BY size DESC LIMIT 20
SELECT path FROM files ORDER BY owner ASC, modified_at DESC
```

Filtering occurs before ordering, and `LIMIT` is applied after ordering. Absolute
path ascending is the final tie-breaker and the default order when `ORDER BY` is
omitted. `LIMIT 0` produces an empty result with its projected columns. Place
`ORDER BY` after `WHERE` and before `LIMIT`. Expressions, aliases, and numeric
column positions are not supported as ordering terms.

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
