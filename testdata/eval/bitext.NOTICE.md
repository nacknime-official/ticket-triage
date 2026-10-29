# Notice for `bitext.jsonl`

`bitext.jsonl` contains data from the **Bitext - Customer Service Tagged Training Dataset for LLM-based
Virtual Assistants**, (c) Bitext Innovations, 2024,
https://huggingface.co/datasets/bitext/Bitext-customer-support-llm-chatbot-training-dataset
(file `Bitext_Sample_Customer_Support_Training_Dataset_27K_responses-v11.csv`, revision
`430d1a89bd93bd1fa23c16f29dd53e73f0087443`).

The data is published under the Community Data License Agreement – Sharing, Version 1.0. The full text is in
`bitext.LICENSE.txt`. `bitext.jsonl` is published under the same agreement.

**This file was changed from the original data.** `cmd/eval-import` made these changes:

- kept a deterministic sample of 30 rows from selected intents and dropped the other rows;
- dropped the `flags`, `category`, `intent` and `response` columns;
- replaced the `{{...}}` placeholders in `instruction` with invented values and stored the result as `body`;
- added `id`, `source`, `lang`, a `subject` cut from the body, and the labels `expected_category`,
  `expected_complaint` and `expected_urgency` drafted for this project.
