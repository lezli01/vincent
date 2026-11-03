# Summary

The importer drops the **first** line of a *multi-line* body. See [ACME-12](https://acme.atlassian.net/browse/ACME-12) and [the docs](https://example.com/docs?a=1&b=2).

Reported by Ada Lovelace.

## Steps to reproduce

1. Create an issue with a body of
   - two lines
   - and a `code` span
2. Run `vincent issue sync`

```go
func main() {
	fmt.Println("hello")
}
```

> The body is the source of truth.

| Field | Value |
| --- | --- |
| status | open ¦ closed |
| priority |  |

---

![screenshot.png](https://acme.atlassian.net/rest/api/3/attachment/content/10001)

[trace.log](https://acme.atlassian.net/secure/attachment/10002/trace.log)