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

import React, { useEffect } from 'react';
import { InputNumber, Select, Typography } from '@douyinfe/semi-ui';
import { Image, SlidersHorizontal } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import {
  image2DefaultSelection,
  image2MaxN,
  image2Qualities,
  image2Sizes,
  isImage2SelectionSupported,
} from '../../helpers/image2Capabilities';

const ImageRequestControls = ({
  inputs,
  onInputChange,
  disabled = false,
  capability,
}) => {
  const { t } = useTranslation();
  const operation =
    inputs.imageEnabled &&
    (inputs.imageUrls || []).some((url) => String(url).trim() !== '')
      ? 'edits'
      : 'generations';
  const sizes = image2Sizes(capability, operation);
  const size = inputs.imageSize;
  const quality = inputs.imageQuality;
  const qualities = image2Qualities(capability, operation, size);
  const maxN = image2MaxN(capability, operation, size, quality);
  const ready = isImage2SelectionSupported(
    capability,
    operation,
    size,
    quality,
    inputs.imageN,
  );

  useEffect(() => {
    const defaults = image2DefaultSelection(capability, operation);
    if (!defaults) return;
    const nextSizes = image2Sizes(capability, operation);
    const nextQualities = image2Qualities(capability, operation, size);
    if (!nextSizes.includes(size) || !nextQualities.includes(quality)) {
      onInputChange('imageSize', defaults.size);
      onInputChange('imageQuality', defaults.quality);
      onInputChange('imageN', defaults.n);
      return;
    }
    const nextMaxN = image2MaxN(capability, operation, size, quality);
    if (nextMaxN > 0 && Number(inputs.imageN) > nextMaxN) {
      onInputChange('imageN', nextMaxN);
    }
  }, [capability, operation, size, quality, inputs.imageN, onInputChange]);

  return (
    <div className={`space-y-4 ${disabled ? 'opacity-50' : ''}`}>
      <div className='flex items-center gap-2'>
        <Image size={16} className='text-blue-500' />
        <Typography.Text strong className='text-sm'>
          {t('Image2 图片参数')}
        </Typography.Text>
      </div>
      <Typography.Text className='text-xs text-gray-500 block'>
        {ready
          ? t('选项来自当前分组能力；不会把 auto 静默升级为 4K。')
          : t('当前组合不可用，请等待能力加载或修改参数。')}
      </Typography.Text>

      <div>
        <div className='flex items-center gap-2 mb-2'>
          <SlidersHorizontal size={14} className='text-gray-500' />
          <Typography.Text strong className='text-sm'>
            {t('质量')}
          </Typography.Text>
        </div>
        <Select
          value={inputs.imageQuality}
          onChange={(value) => onInputChange('imageQuality', value)}
          optionList={[...qualities.map((value) => ({ label: value, value }))]}
          style={{ width: '100%' }}
          disabled={disabled || !capability || !ready}
        />
      </div>

      <div>
        <Typography.Text strong className='text-sm block mb-2'>
          {t('尺寸')}
        </Typography.Text>
        <Select
          value={inputs.imageSize}
          onChange={(value) => onInputChange('imageSize', value)}
          optionList={[...sizes.map((value) => ({ label: value, value }))]}
          style={{ width: '100%' }}
          disabled={disabled || !capability || !ready}
        />
      </div>

      <div>
        <Typography.Text strong className='text-sm block mb-2'>
          {t('生成数量')}
        </Typography.Text>
        <InputNumber
          value={inputs.imageN}
          onNumberChange={(value) => onInputChange('imageN', value || 1)}
          min={1}
          max={maxN || 1}
          precision={0}
          style={{ width: '100%' }}
          disabled={disabled || !capability || !ready}
        />
      </div>
    </div>
  );
};

export default ImageRequestControls;
