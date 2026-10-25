"""ACL-filtered FTS5 retrieval over the actual checked-in document corpus."""

import re

from core import APIError, Route, STAFF, one, require, rows


def visible(actor):
    # Filter candidates inside SQL before ranking or returning any content.
    return "(d.classification='public' OR instr(',' || d.audience || ',', ?) > 0 OR ?='admin')", (
        "," + actor.role + ",", actor.role,
    )


def catalog(db, actor, params, query, body):
    clause, args = visible(actor)
    return rows(db, "SELECT d.id,d.title,d.classification,d.source FROM documents d WHERE " + clause + " ORDER BY d.id", args)


def document(db, actor, params, query, body):
    clause, args = visible(actor)
    return one(db, "SELECT d.* FROM documents d WHERE d.id=? AND " + clause, (params["id"],) + args)


def search(db, actor, params, query, body):
    terms = re.findall(r"\w+", query.get("q", ""), flags=re.UNICODE)
    require(0 < len(terms) <= 20, "Supply q with 1 to 20 search terms", 400, "invalid_request")
    try:
        limit = int(query.get("limit", "5"))
    except ValueError as exc:
        raise APIError(400, "invalid_request", "limit must be an integer") from exc
    require(1 <= limit <= 20, "limit must be 1 to 20", 400, "invalid_request")
    # Literal terms, never executable FTS syntax from the client.
    match = " AND ".join('"' + term + '"' for term in terms)
    clause, args = visible(actor)
    hits = rows(db, "SELECT d.id,d.title,d.classification,d.source,d.body "
                "FROM document_search JOIN documents d ON d.rowid=document_search.rowid "
                "WHERE document_search MATCH ? AND " + clause + " ORDER BY bm25(document_search),d.id LIMIT ?",
                (match,) + args + (limit,))
    # Small, stable paragraph chunks with citations that resolve to authorized document GETs.
    results = []
    for hit in hits:
        paragraphs = [p.strip() for p in hit.pop("body").split("\n\n") if p.strip()]
        ranked = sorted(enumerate(paragraphs, 1), key=lambda item: (-sum(term.lower() in item[1].lower() for term in terms), item[0]))
        number, content = ranked[0]
        results.append({**hit, "chunk_id": f'{hit["id"]}#p{number}', "text": content,
                        "citation": f'/documents/library/{hit["id"]}#p{number}'})
    return {"query": query["q"], "results": results, "retrieval": "sqlite-fts5; literal AND terms"}


ROUTES = [
    Route("GET", "/documents/library", ("public", "customer") + STAFF, catalog),
    Route("GET", "/documents/search", ("public", "customer") + STAFF, search),
    Route("GET", "/documents/library/{id}", ("public", "customer") + STAFF, document),
]
