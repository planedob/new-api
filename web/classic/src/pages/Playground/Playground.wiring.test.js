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

import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { beforeAll, expect, mock, test } from 'bun:test';

const helpersModulePath = new URL('../../helpers/index.js', import.meta.url)
  .pathname;

const api = {
  get: async () => ({
    data: {
      success: true,
      data: { generations: [{ size: '1024x1024' }] },
    },
  }),
};

// The real loader imports the aggregate helpers module, whose other exports
// pull browser-only UI code into a Node test. Keep the loader itself real and
// replace only its transport/normalization seams.
mock.module(helpersModulePath, () => ({
  API: api,
  processModelsData: () => ({ modelOptions: [], selectedModel: '' }),
  processGroupsData: () => [],
  showError: () => {},
  getLogo: () => 'logo',
  stringToColor: () => '#000000',
  buildMessageContent: (content) => content,
  createMessage: (role, content) => ({ role, content }),
  createLoadingAssistantMessage: () => ({ status: 'loading' }),
  getTextContent: () => '',
  buildApiPayload: () => ({}),
  buildImagePayload: () => ({}),
  isGptImage2Model: () => true,
  encodeToBase64: (value) => value,
}));

let useDataLoader;

beforeAll(async () => {
  ({ useDataLoader } = await import('../../hooks/playground/useDataLoader.js'));
});

const createLoaderInputs = () => ({
  model: 'gpt-image-2',
  group: '44',
});

// This is a minimal real React component harness: it mounts the production
// useDataLoader hook, invokes its real Image2 capability request, and records
// the same setter that Playground passes from usePlaygroundState.
const CapabilityLoaderHarness = ({ setImage2Capability, onLoad }) => {
  const loader = useDataLoader(
    { user: { group: 'default' } },
    createLoaderInputs(),
    () => {},
    () => {},
    () => {},
    setImage2Capability,
  );

  onLoad(loader.loadImage2Capability());

  return React.createElement(
    'output',
    { 'data-capability-loader': 'mounted' },
    'Image2 capability loader mounted',
  );
};

test('descendant wiring mounts the real loader and applies capability success', async () => {
  const capability = { generations: [{ size: '3840x2160' }] };
  const setterCalls = [];
  const requests = [];
  let loadPromise;

  api.get = async (url, config) => {
    requests.push({ url, config });
    return { data: { success: true, data: capability } };
  };

  const markup = renderToStaticMarkup(
    React.createElement(CapabilityLoaderHarness, {
      setImage2Capability: (value) => setterCalls.push(value),
      onLoad: (promise) => {
        loadPromise = promise;
      },
    }),
  );

  await loadPromise;

  expect(markup).toContain('data-capability-loader="mounted"');
  expect(requests).toEqual([
    {
      url: '/api/user/image2/capabilities',
      config: { params: { group: '44', model: 'gpt-image-2' } },
    },
  ]);
  expect(setterCalls).toEqual([capability]);
});

test('descendant wiring clears capability on a loader failure without throwing', async () => {
  const setterCalls = [];
  let loadPromise;

  api.get = async () => {
    throw new Error('capability service unavailable');
  };

  expect(() =>
    renderToStaticMarkup(
      React.createElement(CapabilityLoaderHarness, {
        setImage2Capability: (value) => setterCalls.push(value),
        onLoad: (promise) => {
          loadPromise = promise;
        },
      }),
    ),
  ).not.toThrow();

  await loadPromise;
  expect(setterCalls).toEqual([null]);
});
