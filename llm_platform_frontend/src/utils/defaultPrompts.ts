// buildDefaultPrompts derives a concrete starting system prompt and user-prompt
// template from a task's name, description, and input/output JSON Schemas — so
// authors start from a working, task-specific draft instead of a blank page or a
// vague placeholder. The output is meant to be edited.
//
// The task DESCRIPTION is the heart of it: it becomes the standing instruction
// in the system prompt and the concrete ask in the user prompt. The output
// directive is a precise, format-specific sentence (not "return the value
// described above"). The user-prompt template uses Go text/template syntax
// ({{.field}}) to match the backend renderer; only fields declared in the input
// schema are referenced (the renderer fills declared-but-absent fields with "",
// so this is always safe), and optional fields are guarded with {{if .field}}.

type JsonSchema = Record<string, unknown>;

type Field = { name: string; schema: JsonSchema; required: boolean };

function fieldsOf(schema: JsonSchema | null | undefined): Field[] {
  const props = (schema?.properties as Record<string, JsonSchema> | undefined) ?? {};
  const required = new Set<string>((schema?.required as string[] | undefined) ?? []);
  return Object.entries(props).map(([name, s]) => ({
    name,
    schema: s ?? {},
    required: required.has(name),
  }));
}

function titleCase(s: string): string {
  return s.replace(/[_-]+/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

function typeOf(schema: JsonSchema | null | undefined): string | null {
  return schema && typeof schema.type === 'string' ? (schema.type as string) : null;
}

function arrayItemType(schema: JsonSchema): string {
  const items = schema.items as JsonSchema | undefined;
  return items && typeof items.type === 'string' ? items.type : 'item';
}

// isImageField reports whether a field carries image input — a string (or an
// array's items) tagged format:"image", or the implicit image/images names.
// Image values must never be inlined into the prompt with {{.field}}: they are
// (often huge) base64 data URLs that travel to the model as attachments, so the
// generated template only *gates* on their presence.
function isImageField(f: Field): boolean {
  const s = f.schema;
  if (s.type === 'string' && s.format === 'image') return true;
  if (s.type === 'array') {
    const items = s.items as JsonSchema | undefined;
    if (items && items.format === 'image') return true;
  }
  const n = f.name.toLowerCase();
  return n === 'image' || n === 'images';
}

function hasFields(schema: JsonSchema | null | undefined): boolean {
  const props = schema?.properties as Record<string, unknown> | undefined;
  return !!props && Object.keys(props).length > 0;
}

// ensureSentence trims and guarantees terminal punctuation, so a description
// reads cleanly when spliced into a prompt.
function ensureSentence(s: string): string {
  const t = s.trim().replace(/\s+/g, ' ');
  if (!t) return '';
  return /[.!?]$/.test(t) ? t : `${t}.`;
}

// canBuildDefaultPrompts gates the affordance: there must be something concrete
// to generate from — a description to anchor the task, input fields to surface,
// or an output contract to instruct against.
export function canBuildDefaultPrompts(
  taskDescription: string | null | undefined,
  inputSchema: JsonSchema | null | undefined,
  outputSchema: JsonSchema | null | undefined,
): boolean {
  return (
    !!taskDescription?.trim() ||
    hasFields(inputSchema) ||
    !!(outputSchema && Object.keys(outputSchema).length > 0)
  );
}

// outputContract returns the concrete, format-specific instruction for the
// system prompt — phrased as a direct command, never a vague "return the X
// described above".
function outputContract(outputSchema: JsonSchema | null | undefined): string {
  const t = typeOf(outputSchema);
  if (outputSchema && t === 'object' && hasFields(outputSchema)) {
    const bullets = fieldsOf(outputSchema)
      .map((f) => {
        const ft = typeOf(f.schema) ?? 'string';
        const desc = typeof f.schema.description === 'string' ? ` — ${f.schema.description}` : '';
        return `- "${f.name}" (${ft})${f.required ? ', required' : ', optional'}${desc}`;
      })
      .join('\n');
    return [
      'Respond with a single JSON object that conforms exactly to the schema below.',
      'Output only the JSON — no markdown fences, no explanation, and no keys beyond those in the schema:',
      JSON.stringify(outputSchema, null, 2),
      '',
      'Fields:',
      bullets,
    ].join('\n');
  }
  switch (t) {
    case 'string':
      return 'Respond with only the answer as plain text. Do not add quotes, field labels, JSON, or any commentary.';
    case 'integer':
      return 'Respond with only the resulting whole number (digits only). Do not add units, labels, or any other text.';
    case 'number':
      return 'Respond with only the resulting number (digits only, decimal point if needed). Do not add units, labels, or any other text.';
    case 'boolean':
      return 'Respond with only `true` or `false`, lowercase, and nothing else.';
    case 'array':
      return `Respond with only a JSON array of ${arrayItemType(outputSchema as JsonSchema)} values. Output only the array — no surrounding object, prose, or markdown.`;
    default:
      return 'Respond with the answer only — concise and direct, with no preamble, explanation, or commentary.';
  }
}

// closingDirective is the final, format-specific imperative on the user turn.
function closingDirective(outputSchema: JsonSchema | null | undefined): string {
  const t = typeOf(outputSchema);
  if (t === 'object' && hasFields(outputSchema)) return 'Respond with the JSON object now.';
  switch (t) {
    case 'integer':
    case 'number':
      return 'Respond with the number now.';
    case 'boolean':
      return 'Answer with true or false now.';
    case 'array':
      return 'Respond with the JSON array now.';
    default:
      return 'Provide your answer now.';
  }
}

export function buildDefaultPrompts(opts: {
  taskName?: string;
  taskDescription?: string;
  inputSchema?: JsonSchema | null;
  outputSchema?: JsonSchema | null;
}): { systemPrompt: string; promptTemplate: string } {
  const { taskName, taskDescription, inputSchema, outputSchema } = opts;
  const name = taskName?.trim();
  const description = ensureSentence(taskDescription ?? '');
  const inFields = fieldsOf(inputSchema);

  // ── System prompt: who the model is, what the task is, and the exact output ──
  const sys: string[] = [
    name
      ? `You are an expert assistant responsible for the "${name}" task.`
      : 'You are an expert, careful assistant.',
  ];
  if (description) sys.push('', `Your task: ${description}`);
  if (inFields.length) {
    sys.push(
      '',
      'You will be given:',
      ...inFields.map((f) => {
        const ft = isImageField(f) ? 'image' : (typeOf(f.schema) ?? 'string');
        const desc = typeof f.schema.description === 'string' ? ` — ${f.schema.description}` : '';
        return `- ${titleCase(f.name)} (${ft})${f.required ? '' : ', optional'}${desc}`;
      }),
    );
  }
  sys.push('', outputContract(outputSchema));

  // ── User prompt: restate the concrete ask, then the input, then the command ─
  const tpl: string[] = [];
  tpl.push(
    description
      ? description
      : name
        ? `Carry out the ${name} task on the input below.`
        : 'Carry out the task on the input below.',
  );
  if (inFields.length) {
    tpl.push('', 'Input:');
    for (const f of inFields) {
      const label = titleCase(f.name);
      if (isImageField(f)) {
        // Never inline image bytes — gate on presence only; the image itself is
        // sent to the model as an attachment.
        tpl.push(`{{if .${f.name}}}- ${label}: see the attached image(s).{{end}}`);
        continue;
      }
      const line = `- ${label}: {{.${f.name}}}`;
      tpl.push(f.required ? line : `{{if .${f.name}}}- ${label}: {{.${f.name}}}{{end}}`);
    }
  }
  tpl.push('', closingDirective(outputSchema));

  return { systemPrompt: sys.join('\n'), promptTemplate: tpl.join('\n') };
}
