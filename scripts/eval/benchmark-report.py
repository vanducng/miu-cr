#!/usr/bin/env python3
import argparse
import json
import statistics
from pathlib import Path


def load_json(path):
    return json.loads(Path(path).read_text())


def envelope_data(obj):
    return obj.get("data", obj)


def fmt_seconds(ms):
    return f"{ms / 1000:.2f}s"


def stat_seconds(stats, key):
    value = stats.get(key)
    if isinstance(value, (int, float)):
        return fmt_seconds(value)
    return ""


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--result", required=True)
    parser.add_argument("--cases", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    result = envelope_data(load_json(args.result))
    cases = {c.get("id"): c for c in load_json(args.cases).get("cases", []) if c.get("id")}
    lines = [
        "# miu-cr Evaluation Report",
        "",
        f"Cases: {len(cases)}",
        "",
        "| Tool | Cases | Findings | Matched | Missed | Unmatched | Failed | Median | Average |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for tool in result.get("tools", []):
        durations = [c.get("duration_ms", 0) for c in tool.get("cases", [])]
        median = statistics.median(durations) if durations else 0
        average = statistics.mean(durations) if durations else 0
        summary = tool.get("summary", {})
        lines.append(
            "| {name} | {cases} | {found} | {matched} | {missed} | {fp} | {failed} | {median} | {average} |".format(
                name=tool.get("name", ""),
                cases=summary.get("cases", 0),
                found=summary.get("found", 0),
                matched=summary.get("matched", 0),
                missed=summary.get("missed", 0),
                fp=summary.get("false_positives", 0),
                failed=summary.get("failed_cases", 0),
                median=fmt_seconds(median),
                average=fmt_seconds(average),
            )
        )

    lines.extend(["", "## Cases", ""])
    for tool in result.get("tools", []):
        lines.extend([f"### {tool.get('name', '')}", ""])
        lines.append("| Case | Language | Findings | Matched | Missed | Failed | Duration | Context | Provider |")
        lines.append("| --- | --- | ---: | ---: | ---: | --- | ---: | ---: | ---: |")
        for case in tool.get("cases", []):
            meta = cases.get(case.get("id", ""), {})
            failed = "yes" if case.get("error") else ""
            stats = case.get("stats", {})
            lines.append(
                "| {id} | {lang} | {found} | {matched} | {missed} | {failed} | {duration} | {context} | {provider} |".format(
                    id=case.get("id", ""),
                    lang=meta.get("language", ""),
                    found=case.get("score", {}).get("found", 0),
                    matched=case.get("score", {}).get("matched", 0),
                    missed=case.get("score", {}).get("missed", 0),
                    failed=failed,
                    duration=fmt_seconds(case.get("duration_ms", 0)),
                    context=stat_seconds(stats, "context_ms"),
                    provider=stat_seconds(stats, "provider_ms"),
                )
            )
        lines.append("")

    Path(args.out).write_text("\n".join(lines).rstrip() + "\n")


if __name__ == "__main__":
    main()
