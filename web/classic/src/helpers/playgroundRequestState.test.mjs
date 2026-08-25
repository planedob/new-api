/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import assert from 'node:assert/strict';
import test from 'node:test';

import {
  countValidImageSources,
  createLatestInputStore,
  createRequestAbortRegistry,
  getEnabledImageSources,
  resolveImagePreviewPrompt,
} from './playgroundRequestState.js';

test('blank image inputs are not counted as attached images', () => {
  assert.equal(countValidImageSources(['', '   ', null]), 0);
  assert.equal(
    countValidImageSources(['', 'data:image/png;base64,iVBORw0KGgo=', '   ']),
    1,
  );
});

test('latest input store exposes pasted images to a previously captured sender', () => {
  const store = createLatestInputStore({
    model: 'gpt-image-2',
    imageEnabled: true,
    imageUrls: [],
  });
  const capturedSender = () => store.get();

  store.update('imageUrls', ['data:image/png;base64,iVBORw0KGgo=']);

  assert.deepEqual(capturedSender().imageUrls, [
    'data:image/png;base64,iVBORw0KGgo=',
  ]);
  assert.deepEqual(getEnabledImageSources(capturedSender()), [
    'data:image/png;base64,iVBORw0KGgo=',
  ]);
});

test('image preview uses the current draft instead of the previous submitted prompt', () => {
  assert.equal(
    resolveImagePreviewPrompt(' current request ', 'previous request'),
    'current request',
  );
  assert.equal(
    resolveImagePreviewPrompt('', ' previous request '),
    'previous request',
  );
  assert.equal(resolveImagePreviewPrompt('  ', ''), '示例图片提示词');
});

test('request abort registry aborts the active non-stream request exactly once', () => {
  const registry = createRequestAbortRegistry();
  const controller = registry.begin();

  assert.equal(controller.signal.aborted, false);
  assert.equal(registry.abort(), true);
  assert.equal(controller.signal.aborted, true);
  assert.equal(registry.abort(), false);
});

test('clearing an old request does not detach a newer active request', () => {
  const registry = createRequestAbortRegistry();
  const first = registry.begin();
  const second = registry.begin();

  assert.equal(first.signal.aborted, true);
  registry.clear(first);
  assert.equal(registry.abort(), true);
  assert.equal(second.signal.aborted, true);
});
