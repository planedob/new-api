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
import { MemoryRouter } from 'react-router-dom';
import { beforeAll, beforeEach, expect, mock, test } from 'bun:test';

const componentPath = new URL('./index.jsx', import.meta.url).pathname;
const userContextPath = new URL('../../context/User/index.jsx', import.meta.url)
  .pathname;
const mobileHookPath = new URL(
  '../../hooks/common/useIsMobile.js',
  import.meta.url,
).pathname;
const stateHookPath = new URL(
  '../../hooks/playground/usePlaygroundState.js',
  import.meta.url,
).pathname;
const apiRequestHookPath = new URL(
  '../../hooks/playground/useApiRequest.jsx',
  import.meta.url,
).pathname;
const messageActionsHookPath = new URL(
  '../../hooks/playground/useMessageActions.jsx',
  import.meta.url,
).pathname;
const syncHookPath = new URL(
  '../../hooks/playground/useSyncMessageAndCustomBody.js',
  import.meta.url,
).pathname;
const messageEditHookPath = new URL(
  '../../hooks/playground/useMessageEdit.jsx',
  import.meta.url,
).pathname;
const dataLoaderHookPath = new URL(
  '../../hooks/playground/useDataLoader.js',
  import.meta.url,
).pathname;
const helpersPath = new URL('../../helpers/index.js', import.meta.url).pathname;
const optimizedComponentsPath = new URL(
  '../../components/playground/OptimizedComponents.js',
  import.meta.url,
).pathname;
const chatAreaPath = new URL(
  '../../components/playground/ChatArea.jsx',
  import.meta.url,
).pathname;
const floatingButtonsPath = new URL(
  '../../components/playground/FloatingButtons.jsx',
  import.meta.url,
).pathname;
const playgroundContextPath = new URL(
  '../../contexts/PlaygroundContext.jsx',
  import.meta.url,
).pathname;

const setterCalls = [];
let loaderMode = 'success';
let loaderArgs;

const noop = () => {};
const passthrough = ({ children }) =>
  React.createElement('div', null, children);
const Layout = Object.assign(passthrough, {
  Sider: passthrough,
  Content: passthrough,
});

mock.module('@douyinfe/semi-ui', () => ({
  Layout,
  Modal: passthrough,
  Toast: { error: noop, warning: noop },
}));

mock.module('react-i18next', () => ({
  useTranslation: () => ({
    t: (key) => key,
    i18n: { language: 'en' },
  }),
}));

mock.module(userContextPath, () => ({
  UserContext: React.createContext([
    { user: { username: 'wiring-test-user', group: 'default' } },
    noop,
  ]),
}));

mock.module(mobileHookPath, () => ({ useIsMobile: () => false }));

const createPlaygroundState = () => ({
  inputs: {
    model: 'gpt-image-2',
    group: '44',
    imageEnabled: false,
    imageUrls: [],
    imageN: 1,
    imageSize: 'auto',
    imageQuality: 'auto',
    stream: true,
  },
  parameterEnabled: {},
  showDebugPanel: false,
  customRequestMode: false,
  customRequestBody: '',
  showSettings: false,
  models: [],
  groups: [],
  image2Capability: null,
  status: {},
  message: [],
  debugData: {},
  activeDebugTab: 'preview',
  previewPayload: null,
  sseSourceRef: { current: null },
  chatRef: { current: null },
  handleInputChange: noop,
  handleParameterToggle: noop,
  debouncedSaveConfig: noop,
  saveMessagesImmediately: noop,
  handleConfigImport: noop,
  handleConfigReset: noop,
  setShowSettings: noop,
  setModels: noop,
  setGroups: noop,
  setImage2Capability: (value) => setterCalls.push(value),
  setStatus: noop,
  setMessage: noop,
  setDebugData: noop,
  setActiveDebugTab: noop,
  setPreviewPayload: noop,
  setShowDebugPanel: noop,
  setCustomRequestMode: noop,
  setCustomRequestBody: noop,
});

mock.module(stateHookPath, () => ({
  usePlaygroundState: () => createPlaygroundState(),
}));

mock.module(apiRequestHookPath, () => ({
  useApiRequest: () => ({ sendRequest: noop, onStopGenerator: noop }),
}));

mock.module(messageActionsHookPath, () => ({
  useMessageActions: () => ({
    handleMessageReset: noop,
    handleMessageCopy: noop,
    handleMessageDelete: noop,
    handleRoleToggle: noop,
  }),
}));

mock.module(syncHookPath, () => ({
  useSyncMessageAndCustomBody: () => ({
    syncMessageToCustomBody: noop,
    syncCustomBodyToMessage: noop,
  }),
}));

mock.module(messageEditHookPath, () => ({
  useMessageEdit: () => ({
    editingMessageId: null,
    editValue: '',
    setEditValue: noop,
    handleMessageEdit: noop,
    handleEditSave: noop,
    handleEditCancel: noop,
  }),
}));

mock.module(dataLoaderHookPath, () => ({
  useDataLoader: (...args) => {
    loaderArgs = args;
    const setImage2Capability = args[5];
    if (typeof setImage2Capability !== 'function') {
      throw new TypeError('Playground did not wire setImage2Capability');
    }
    setImage2Capability(
      loaderMode === 'success'
        ? { generations: [{ size: '3840x2160' }] }
        : null,
    );
    return {};
  },
}));

mock.module(helpersPath, () => ({
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

const componentStub = ({ children }) =>
  React.createElement('section', null, children);

mock.module(optimizedComponentsPath, () => ({
  OptimizedSettingsPanel: componentStub,
  OptimizedDebugPanel: componentStub,
  OptimizedMessageContent: componentStub,
  OptimizedMessageActions: componentStub,
}));
mock.module(chatAreaPath, () => ({ default: componentStub }));
mock.module(floatingButtonsPath, () => ({ default: componentStub }));
mock.module(playgroundContextPath, () => ({
  PlaygroundProvider: componentStub,
}));

let Playground;

beforeAll(async () => {
  ({ default: Playground } = await import(componentPath));
});

beforeEach(() => {
  setterCalls.length = 0;
  loaderArgs = undefined;
  loaderMode = 'success';
});

const renderPlayground = () =>
  renderToStaticMarkup(
    React.createElement(MemoryRouter, null, React.createElement(Playground)),
  );

test('production Playground import and render execute the Image2 loader wiring', () => {
  const markup = renderPlayground();

  expect(markup).toContain('h-full');
  expect(loaderArgs).toHaveLength(6);
  expect(typeof loaderArgs[5]).toBe('function');
  expect(setterCalls).toEqual([{ generations: [{ size: '3840x2160' }] }]);
});

test('production Playground capability failure path remains render-safe', () => {
  loaderMode = 'failure';

  expect(() => renderPlayground()).not.toThrow();
  expect(loaderArgs).toHaveLength(6);
  expect(setterCalls).toEqual([null]);
});
