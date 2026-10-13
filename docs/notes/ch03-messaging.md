# Chapter 3: Two services, one queue

## The idea
A queue decouples services in time: the producer doesn't wait for the consumer, and neither needs the other to be running. Kill one, and the other keeps working.

## Key points
- **HTTP or a queue?**
  - Use HTTP when you need the answer now (is this in stock?).
  - Use a queue when the work can happen later, when a downstream failure must not fail the caller, when several services react to one event, or to absorb bursts.
- **NATS Core is at-most-once.** If nobody is subscribed, the message is gone. A consumer restarts "from now" and never sees what it missed. Durable delivery needs JetStream or Kafka (Chapter 8).
- **A queue group** (`QueueSubscribe`) delivers each message to exactly one member. That's how you scale consumers horizontally without changing code.
- **A plain subscriber** with no group gets a copy of every message, which is fan-out. The dashboard uses this.
- **The coupling that remains:** the broker, the subject name and the message schema. Treat subject plus schema like a public API, and version it (`orders.created.v2`).
- **Interfaces first:** services depend on `messaging.Publisher` and `Subscriber`, never on `nats.Conn`. That makes the broker swappable and testable.

## Rules of thumb
- Make consumers **idempotent**: processing the same message twice has the same effect as once.
- Log a message's age (`age_ms`) in consumers. If it grows, the consumer is falling behind.
- Configure reconnects (`MaxReconnects(-1)`) and log disconnect and reconnect events. "Healthy but no messages" incidents are hard to debug otherwise.
- In one repo, a shared `events` package is fine. Across repos, use schemas (JSON Schema or protobuf), not shared Go types.

## Traps
- **The NATS client buffers publishes during a short disconnect** and sends them on reconnect. One message published while NATS restarted still arrived. That's not durability; it only covers a brief outage while the publisher stays up.
- **A consumer kept running after its NATS was stopped.** With unlimited reconnects, it attached itself to a new NATS on the same port. Resilience works, which also means stray processes come back.

## In this repo
`platform/messaging` (interfaces), `platform/messaging/nats`, `cmd/producer`, `cmd/consumer`, `events/`. Try `make run-consumers` to see the queue group split.

## Check yourself
1. A consumer is down for 10 seconds with NATS Core. What happens to messages published in that time?
2. How do you double consumer throughput without changing code?
3. Why is renaming a subject a breaking change?
