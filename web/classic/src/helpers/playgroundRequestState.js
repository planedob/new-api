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

export const createLatestInputStore = (initialInputs = {}) => {
  let current = initialInputs;

  return {
    get: () => current,
    replace: (nextInputs) => {
      current = nextInputs;
      return current;
    },
    update: (name, value) => {
      current = { ...current, [name]: value };
      return current;
    },
  };
};

export const getEnabledImageSources = (inputs = {}, attachment = null) => {
  const imageUrls = (inputs.imageEnabled ? inputs.imageUrls || [] : []).filter(
    (source) => typeof source === 'string' && source.trim() !== '',
  );
  return imageUrls.length > 0 ? imageUrls : attachment ? [attachment] : [];
};

export const countValidImageSources = (sources = []) =>
  sources.filter((source) => typeof source === 'string' && source.trim() !== '')
    .length;

export const createRequestAbortRegistry = () => {
  let activeController = null;

  return {
    begin: () => {
      if (activeController && !activeController.signal.aborted) {
        activeController.abort();
      }
      const controller = new AbortController();
      activeController = controller;
      return controller;
    },
    clear: (controller) => {
      if (activeController === controller) activeController = null;
    },
    abort: () => {
      if (!activeController || activeController.signal.aborted) return false;
      const controller = activeController;
      activeController = null;
      controller.abort();
      return true;
    },
  };
};
