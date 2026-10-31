# Pinned Go self-review sources

These files are source-analysis fixtures from the commit recorded in `manifest.json`. Complete files retain their original bytes. Function extracts retain their original function bodies and add only a package/import envelope.

The extracts are intentionally not a buildable copy of the application. Tests analyze their syntax and pinned dependencies in a temporary repository. They never build or execute the fixture source.
