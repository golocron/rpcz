# rpcz

RPCz is a library for RPC-style interservice communication over network. It's simple, lightweight yet performant. It provides you with the infrastructure for building services and clients, taking care of the low level details such as connection handling and lifecycle management.

It's designed to work over plain TCP or Unix sockets, or with TLS on top of it. The primary encoding is Protobuf, with JSON available as an alternative. Adjustable bufferring helps achieve better throughput based on the needs of your application.

While providing similar functionality to gRPC, Twirp and the standard library's `rpc` package, RPCz differs from each in some way:
- it's lightweight
- it focuses on solving one particular task – performant communication over TCP
- the public API is minimal, and consists primarily of a handful of convenience constructors
- supports only two encodings
- it handles connections gracefully, and supports (even enforces) use of `context.Context`.

The library code uses only one external dependency, Protobuf.


## Examples

Examples of a server running an RPC service, and a client that interacts with it is available in [this repository](https://github.com/golocron/rpcz-example).


## In Other Languages

The most obvious uses case is communication between services developed in Go. However, one of the main reasons for creating this library was the need to interact between software written in different languages.

The simple protocol of RPCz and the use of Protobuf allow for such cross-language comminication. A server implementing the RPCz protocol must accept and correctly handle a request from a client implementing the protocol.

| Language | Server | Client | Status |
|--- | --- | --- | --- |
| Go | ✅ | ✅ | Initial release |
| Rust | ⚪ | ⚪ | Planned |
| Zig | ⚪ | ⚪ | Planned |

There are currently no plans on implementations for languages other than the listed above.


## Benchmarks

The results below were obtained by running:

```bash
go test -benchmem -benchtime 5s -bench=Benchmark -count 5 -timeout 600s -cpu=8 | tee results.bench

benchstat results.bench
```

```text
name                time/op
Protobuf_1K-8         13.4µs ± 7%
Protobuf_4K-8         19.2µs ± 1%
Protobuf_16K-8        38.0µs ± 3%
Protobuf_32K-8        65.9µs ± 2%
Protobuf_64K-8        91.6µs ±14%
ProtobufTLS_4K-8      19.6µs ± 3%
ProtobufNoBuf_4K-8    25.9µs ± 5%
JSON_1K-8             20.4µs ± 4%
JSON_4K-8             53.6µs ± 5%
JSON_16K-8             185µs ± 2%
JSONTLS_4K-8          55.9µs ± 2%

name                speed
Protobuf_1K-8        153MB/s ± 7%
Protobuf_4K-8        427MB/s ± 1%
Protobuf_16K-8       862MB/s ± 3%
Protobuf_32K-8      1.00GB/s ± 2%
Protobuf_64K-8      1.44GB/s ±13%
ProtobufTLS_4K-8     417MB/s ± 3%
ProtobufNoBuf_4K-8   317MB/s ± 5%
JSON_1K-8            100MB/s ± 4%
JSON_4K-8            153MB/s ± 5%
JSON_16K-8           177MB/s ± 2%
JSONTLS_4K-8         147MB/s ± 2%

name                alloc/op
Protobuf_1K-8         9.42kB ± 0%
Protobuf_4K-8         40.1kB ± 0%
Protobuf_16K-8         155kB ± 0%
Protobuf_32K-8         333kB ± 0%
Protobuf_64K-8         614kB ± 0%
ProtobufTLS_4K-8      37.9kB ± 0%
ProtobufNoBuf_4K-8    47.6kB ± 0%
JSON_1K-8             5.56kB ± 0%
JSON_4K-8             19.2kB ± 0%
JSON_16K-8            72.3kB ± 0%
JSONTLS_4K-8          19.3kB ± 0%

name                allocs/op
Protobuf_1K-8           21.0 ± 0%
Protobuf_4K-8           21.0 ± 0%
Protobuf_16K-8          21.0 ± 0%
Protobuf_32K-8          21.0 ± 0%
Protobuf_64K-8          21.0 ± 0%
ProtobufTLS_4K-8        22.0 ± 0%
ProtobufNoBuf_4K-8      27.0 ± 0%
JSON_1K-8               29.0 ± 0%
JSON_4K-8               29.0 ± 0%
JSON_16K-8              29.0 ± 0%
JSONTLS_4K-8            30.0 ± 0%
```

You can also have a look <a target="_blank" href="https://github.com/cockroachdb/rpc-bench#results-2020-04-18">here</a> for benchmarks of other systems, such as gRPC and the `rpc` package from the standard library.


## Open-Source, not Open-Contribution

Similar to [SQLite](https://www.sqlite.org/copyright.html) and [Litestream](https://github.com/benbjohnson/litestream#open-source-not-open-contribution), RPCz is open-source, but is not open to contributions. This helps keep the code base free of confusions with licenses or proprietary changes, and prevent feature bloat.

In addition, experiences of many open-source projects have shown that maintenance of an open-source code base can be quite resource demanding. Time, mental health and energy – are just a few.

Taking the above into account, I've made the decision to make the project closed to contributions.

Thank you for understanding!
