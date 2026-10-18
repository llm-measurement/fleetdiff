# Dataset Attribution And License Notes

This publication selects public task identifiers and normalized group digests,
and adds scalar usage/cost observations and analysis. It publishes no dataset
task text, reference answers, solutions, tests or generated answers. These
source notices do not relicense the repository or imply upstream endorsement.

## Databricks Dolly 15k

Copyright (2023) Databricks, Inc.
Copyright Wikipedia editors and contributors, for source material in the
closed-QA and information-extraction categories ([Wikipedia](https://www.wikipedia.org/)).

License: [Creative Commons Attribution-ShareAlike 3.0 Unported](https://creativecommons.org/licenses/by-sa/3.0/).
Revision: `bdd27f4d94b9c1f951818a7da7fd7aeea5dbff1a`.
[Pinned dataset card and attribution](https://huggingface.co/datasets/databricks/databricks-dolly-15k/blob/bdd27f4d94b9c1f951818a7da7fd7aeea5dbff1a/README.md).

Changes: selected and partitioned source records; published original line-number
IDs and whitespace-normalized context digests with newly measured scalar usage.
The pinned source file and license-card hashes are in [source-record.json](source-record.json).
The source license and attribution continue to apply to Dolly-derived material.

## Mostly Basic Python Problems (MBPP)

Dataset curator: Google Research. Attribution: *Program Synthesis with Large
Language Models*, Jacob Austin, Augustus Odena, Maxwell Nye, Maarten Bosma,
Henryk Michalewski, David Dohan, Ellen Jiang, Carrie Cai, Michael Terry,
Quoc Le and others (2021), [paper](https://arxiv.org/abs/2108.07732).

License: [Creative Commons Attribution 4.0 International](https://creativecommons.org/licenses/by/4.0/).
Revision: `4bb6404fdc6cacfda99d4ac4205087b89d32030c`, full/test split.
[Pinned dataset card and citation](https://huggingface.co/datasets/google-research-datasets/mbpp/blob/4bb6404fdc6cacfda99d4ac4205087b89d32030c/README.md).

Changes: selected and partitioned records using problem text only; published
original task IDs and whitespace-normalized problem digests with newly measured
scalar usage. Reference code, test lists and labels were excluded from selection
and model inputs. Source and license-card hashes are in [source-record.json](source-record.json).
The source license and attribution continue to apply to MBPP-derived material.

## Historical HumanEval Exclusions

The exclusion index includes public OpenAI HumanEval task IDs and normalized
problem digests from the earlier study; no HumanEval implementation code is
republished. HumanEval revision `6d43fb980f9fee3c892a914eda09951f772ad10d` is MIT,
copyright OpenAI. The existing [HumanEval license notice](../../evidence/model-mix/LICENSES.md#openai-humaneval)
contains the pinned license and full MIT text.
