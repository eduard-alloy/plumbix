# Acceptance criteria and criteria sets

Back to the [README](../README.md).

Acceptance criteria belong with the specification, before the code, owned by the person who owns the intent (A43). A specification tool posts them as a **criteria set** when the spec is settled, and a change only names the set:

```sh
plumbix criteria add -f set.json      # {"task": "kai:plumbix/X6Q5JY", "owner": "eduard@mac", "criteria": [{"text": "...", "check": {"steps": ["test"]}}]}
plumbix criteria list --task kai:plumbix/X6Q5JY
plumbix criteria show cs_abc123def456
```

An API submission names it with `"criteria_set": "cs_…"` and may leave `acceptance_criteria` out; a pull request names it with one line in its description, `Acceptance criteria: cs_…`, and its own checklist is then not used. The change takes the set's criteria frozen at that moment, with the set's id, owner and task recorded on the change and in `change.submitted`; the gate check says where the criteria came from. Once any change has used a set, the set is frozen: `PUT /api/v1/criteria-sets/{id}` answers `criteria_set_used`, and a new set is a new id. A set binds pipeline steps only, never a command, since any change may name it. A pull request can name a set but never write one; a set it names that does not exist leaves the change with the checklist from the description and the problem recorded. Kai publishes a task's criteria with `kai plan <project> publish <ref>`.
