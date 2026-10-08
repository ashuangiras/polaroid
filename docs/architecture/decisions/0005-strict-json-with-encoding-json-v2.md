# 0005. Strict JSON decoding with `encoding/json/v2`

**Status:** Accepted
**Date:** 2026-10-09

## Context

Stored versions are immutable, so a malformed or ambiguous record cannot be fixed later. `encoding/json` v1 matches member names case-insensitively, keeps the last value of a duplicate member and accepts invalid UTF-8. Any of these could store something other than what the author meant. Polaroid must still accept arbitrary task-specific JSON objects in `contract` and `instructions`. In Go 1.27, `encoding/json/v2` and `encoding/json/jsontext` are in the standard library (`api/go1.27.txt`) and need no `GOEXPERIMENT` setting.

## Decision

- The transport decodes request bodies with `encoding/json/v2` and `RejectUnknownMembers(true)`. Member names are matched exactly. Duplicate members, invalid UTF-8 and trailing data are rejected.
- `contract` and `instructions` are held as `jsontext.Value`. The domain accepts any JSON object with unique member names at every level and removes insignificant whitespace. Everything else, including member order, is kept as submitted.
- Responses are encoded with `encoding/json/v2`.

## Consequences

- Clients get a `400` with a precise message for typos, for unknown or planned fields such as `references`, and for ambiguous JSON, instead of silent data loss.
- Polaroid relies on a standard-library package that is new in Go 1.27, which is one reason for the 1.27 minimum ([ADR-0002](0002-go-toolchain-and-provisional-module-path.md)).
- The record contract does not depend on JSON key order, but stored values keep the author's order.
