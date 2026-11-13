# Query language

The initial language is a deliberately small SQL-like subset:

```text
SELECT (* | selection [, selection ...])
FROM files
[WHERE condition]
[GROUP BY field [, field ...]]
[ORDER BY selection [ASC | DESC] [, selection [ASC | DESC] ...]]
[LIMIT non_negative_integer]
[;]

condition = conjunction [OR conjunction ...]
conjunction = condition_term [AND condition_term ...]
condition_term = NOT condition_term | (condition) | predicate
predicate = field operator literal | field IS [NOT] NULL
          | field [NOT] LIKE string [ESCAPE single_character_string]
selection = field | COUNT(*) | aggregate(field)
aggregate = COUNT | SUM | AVG | MIN | MAX
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
| `owner_id` | Nullable string | Native filesystem owner ID; decimal UID text on macOS |
| `owner` | Nullable string | Owner account name resolved at collection time |

`SELECT *` expands to the fields in the order above. Native filesystem identity
is stored internally and is not a query field yet.

Ownership means the filesystem owner, not the document author or original
creator. The account name is resolved once per native owner ID per scan and can be unavailable
even when the numeric UID is known. Ownership of symlinks describes the link.
Version-1 catalogs are rejected; collect into a new catalog directory.
Owner IDs are scoped to their originating system or authority and matching IDs
in different catalogs do not establish the same account.
Unavailable values appear as `null` in JSON and `NULL` in text output. They never
satisfy a comparison, including `!=`. Use `IS NULL` or `IS NOT NULL` to test
availability. These predicates always return true or false; zero and empty
strings are not null. Null tests are accepted for every field.

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
SELECT path, owner_id, owner FROM files WHERE owner = 'root'
SELECT path FROM files WHERE owner_id = '0'
SELECT path FROM files WHERE owner IS NULL
SELECT path FROM files WHERE owner_id IS NOT NULL AND owner IS NULL
```

`NOT` has higher precedence than `AND`, which has higher precedence than `OR`.
Parentheses override precedence. Negating unknown remains unknown. Recursive
condition nesting is limited to 128 levels. Comparisons with null values are
unknown; `WHERE` includes only true results. True OR unknown is true, while
false OR unknown remains unknown.

```sql
SELECT path FROM files WHERE owner = 'root' OR kind = 'directory'
SELECT path FROM files WHERE NOT (kind = 'directory' OR size = 0)
```

## Pattern matching

`LIKE` matches an entire string, case-sensitively. `%` matches zero or more
Unicode characters, and `_` matches exactly one Unicode character, including
newlines. All other characters are literal, including regular expression
punctuation. LIKE patterns must be valid UTF-8. Only string fields support LIKE.

Backslashes remain literal by default. An optional `ESCAPE` clause specifies
exactly one Unicode character; it makes the following pattern character literal,
including `%`, `_`, and the escape character itself. A trailing escape is an error.
`NOT LIKE` negates the match; unavailable values remain unknown and are excluded
unless another condition makes the overall filter true.

```sql
SELECT path FROM files WHERE name LIKE '%.go' OR name LIKE '%.md'
SELECT path FROM files WHERE name NOT LIKE 'test_%'
SELECT path FROM files WHERE name LIKE 'report!_!%.txt' ESCAPE '!'
```

Patterns are compiled once per query using the standard library's regular
expression engine, with literals escaped and no exponential backtracking.
LIKE does not apply filesystem path-component rules: `%` can match `/`, and
`path LIKE '/tmp/app%'` can also match `/tmp/application`. It is not a
path-descendant operator.

## Grouping and aggregates

`GROUP BY` combines matching records with equal values in the named fields.
Multiple fields form a tuple, and unavailable values form a single null group.
Timestamp keys compare by instant, even when their timezone offsets differ;
grouped timestamp output is normalized to UTC.

| Aggregate | Meaning |
| --- | --- |
| `COUNT(*)` | Number of records |
| `COUNT(field)` | Number of non-null values |
| `SUM(field)` | Integer sum of `size` |
| `AVG(field)` | Floating-point mean of `size` |
| `MIN(field)` | Smallest non-null value of any field |
| `MAX(field)` | Largest non-null value of any field |

Aggregates ignore null field values. For empty inputs or all-null values,
`COUNT` returns zero and other aggregates return null. Integer sums outside the
signed 64-bit range fail explicitly; averages avoid integer-sum overflow but use
floating-point output and may round.

```sql
SELECT kind, COUNT(*) FROM files GROUP BY kind
SELECT owner, COUNT(*), SUM(size) FROM files WHERE kind = 'file' GROUP BY owner
SELECT owner, kind, AVG(size), MAX(size) FROM files GROUP BY owner, kind
SELECT COUNT(*), SUM(size) FROM files WHERE kind = 'file'
SELECT owner FROM files GROUP BY owner ORDER BY COUNT(*) DESC LIMIT 10
```

Without `GROUP BY`, aggregates summarize all matching records in one row,
including an empty input. An empty input with `GROUP BY` produces no groups.
Grouping without aggregates returns one row per distinct grouping tuple.

In a grouped or aggregate query, each plain field in `SELECT` or `ORDER BY` must
appear in `GROUP BY`. Grouping fields and aggregate expressions used for ordering
need not appear in `SELECT`. Functions and fields are case-insensitive; aggregate
column labels are normalized, for example `count(*)` and `sum(size)`.

Aggregates cannot appear in `WHERE` or `GROUP BY`. `HAVING`, `DISTINCT`, aliases,
nested aggregates, expressions as grouping keys, and numeric grouping positions
are not supported. `SELECT *` expands to all fields and follows the same grouping
validation rules as an explicit field list.

## Ordering and limits

`ORDER BY` accepts query fields and aggregate expressions, including selections
omitted from `SELECT`, subject to the grouping rules above.
Each field defaults to `ASC` (ascending); use `DESC` for descending order. Multiple
fields are evaluated left to right. Strings sort lexically and case-sensitively,
integers sort numerically, and timestamps sort by instant. Unavailable values
always sort last in both directions. `NULLS FIRST` and `NULLS LAST` syntax is not
supported.

```sql
SELECT path, size FROM files WHERE kind = 'file' ORDER BY size DESC LIMIT 20
SELECT path FROM files ORDER BY owner ASC, modified_at DESC
```

Execution follows `WHERE`, grouping and aggregation, `ORDER BY`, then `LIMIT`.
For ordinary queries, absolute path ascending is the default order and final
tie-breaker. For grouped queries, the grouping tuple in `GROUP BY` order provides
the default order and final tie-breaker, ascending with nulls last.
`LIMIT 0` produces an empty result with its projected columns. Place `ORDER BY`
after `GROUP BY` (or `WHERE` when not grouped) and before `LIMIT`. Arithmetic
expressions, aliases, and numeric column positions are not ordering terms.

Unknown fields, unknown relations, invalid literals, unsupported operators, and
trailing statements produce errors. There is no glob matching,
path-descendant operator, arithmetic, joins, aliases, or live
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

`fyreq clear` removes all persisted records and coverage from the selected catalog
and starts a new, empty catalog at generation 1, even if the old catalog is
incompatible or corrupt. Queries then return empty results (or the
normal aggregate result for empty input) until metadata is collected again. This
does not indicate that the filesystem is empty; inspect `fyreq status` for coverage.

## Interactive shell

`fyreq shell` starts with a one-line reminder of statement execution and `.help`.
The full help lists commands and keyboard shortcuts, including Ctrl-D to exit at
an empty prompt. The shell runs statements after a semicolon outside a
single-quoted string.
A statement can span lines and several statements can appear on one line. The
same doubled-quote and literal-backslash rules described above apply. Meta commands
`.help`, `.status`, and `.exit` occupy a line by themselves with no pending SQL.
An unfinished statement at end of input is an error. Query errors do not end the
session. Query output identifies its catalog generation and `.status` reports
coverage and observation times; no query implicitly scans the filesystem.
