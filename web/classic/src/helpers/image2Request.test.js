import { expect, test } from 'bun:test';
import { normalizeImage2RequestPayload } from './image2Request';

const createForm = () => {
  const form = new FormData();
  form.append('model', 'gpt-image-2');
  form.append('group', 'image2');
  form.append('prompt', 'safe test prompt');
  form.append('n', '1');
  form.append(
    'image',
    new File(['image'], 'reference.png', { type: 'image/png' }),
  );
  return form;
};

test('classic generation normalization drops stale image and remains JSON', () => {
  const payload = normalizeImage2RequestPayload(createForm(), 'generations');
  expect(payload).not.toBeInstanceOf(FormData);
  expect(payload).toEqual({
    model: 'gpt-image-2',
    group: 'image2',
    prompt: 'safe test prompt',
    n: '1',
  });
});

test('classic edits normalization preserves multipart source image', () => {
  const payload = createForm();
  expect(normalizeImage2RequestPayload(payload, 'edits')).toBe(payload);
  expect(payload.get('image')).toBeInstanceOf(File);
});
