Document this backend's HTTP API{{FOCUS}}.

1. Identify the language, framework(s), and how routes are registered; check for an existing OpenAPI/Swagger spec, Postman collection, or prior docs and use them as a cross-check, not as truth.
2. Enumerate every endpoint (including ones mounted under prefixes, versioned routers, and websocket/SSE endpoints if present).
3. Trace each endpoint to its validation, auth, and actual response shape.
4. Write README.md and one endpoints/<resource>.md file per resource following the template. Update existing files rather than discarding accurate content.

Finish with a short summary: number of endpoints documented, files written, and anything you could not verify.
