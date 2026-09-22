# The problem

## What we are trying to measure

[local-llm](https://github.com/evanwtf/local-llm) asks which model + engine +
harness combination best runs a coding agent locally. Speed is one of the three
things it judges on, and "speed" is really two workloads:

- **Prefill**: tokens of prompt processed per second. For an agent, prompts are
  long and grow every turn, so this often dominates wall-clock time.
- **Decode**: tokens generated per second. This is what a user feels while the
  answer streams.

A single "tok/s" figure blends the two and hides which one a stack is good at.
Every figure this project produces reports them separately.

## Why the numbers are missing today

Throughput is collected by a central Prometheus **pulling** from each engine's
own port. That breaks in four ways, all of them silent:

1. **Ports move with the arm.** Different recipes serve on different ports.
   Each new arm needs a new scrape target in the central config and a reload,
   by someone who has write access to the monitoring host. When nobody adds
   the target, the panel stays empty and nobody notices until someone asks for
   the number.
2. **The engines speak different dialects.** vLLM and llama.cpp expose
   Prometheus counters under different names. SGLang has its own names and
   needs a flag. Ollama reports throughput per response, not as counters.
   Some engines expose nothing but a log. A query written for one engine
   returns nothing for another.
3. **Multi-node servers.** A model tensor-parallel across two machines exposes
   metrics on the node serving the API only. Nothing says the series spans two
   machines.
4. **Numbers that nothing can join to a result.** A Grafana panel whose
   series cannot be tied to the benchmark run that produced it is decoration.
   The label set has to carry enough to join a series to a row.

A concrete case: on 2026-09-22 a two-node vLLM arm served for a full
30-trial run and **no decode throughput was recorded at all**, because
Prometheus scraped the usual vLLM port and the recipe served on a different one.
The only record was a manual `curl` of the server's own `/metrics`.

## What done looks like

- Any engine, on any host, produces **the same metric names and labels**.
- A new arm needs **no change on the monitoring host**. It registers itself on
  its own host, and the exporter picks it up.
- **A missing metric is loud.** A registered server that stops answering
  exports `llm_engine_up 0`, which can alert.
- Prefill and decode tok/s come from **counters**, with rates computed at query
  time, and are always reported separately.
- The benchmark harness can read the same counters at the start and end of a
  trial and store the deltas, so a published tok/s and the Grafana panel are
  **one counter read two ways** and cannot disagree.
- Every series carries at least **`engine`** and **`model`** labels (a hard
  requirement), plus enough more to join it to a benchmark row.

## Out of scope

- GPU, power, temperature and host memory. `node_exporter`, DCGM, and the Mac
  SMC exporter already cover these, and this exporter does not duplicate them.
- Running or launching models. The exporter only observes.
- Judging quality. That is the benchmark's job.
