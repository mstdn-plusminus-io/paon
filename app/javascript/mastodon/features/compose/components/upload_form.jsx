import PropTypes from 'prop-types';
import { useCallback, useMemo } from 'react';

import { defineMessages, useIntl } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';

import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  TouchSensor,
  closestCenter,
  useSensor,
  useSensors,
} from '@dnd-kit/core';
import {
  SortableContext,
  rectSortingStrategy,
  sortableKeyboardCoordinates,
} from '@dnd-kit/sortable';

import SensitiveButtonContainer from '../containers/sensitive_button_container';
import UploadContainer from '../containers/upload_container';
import UploadProgressContainer from '../containers/upload_progress_container';

const messages = defineMessages({
  instructions: {
    id: 'upload_form.drag_and_drop.instructions',
    defaultMessage: 'To reorder an attachment, press space or enter, move it with the arrow keys, then press space or enter again to drop it. Press escape to cancel.',
  },
  pickedUp: {
    id: 'upload_form.drag_and_drop.on_drag_start',
    defaultMessage: 'Picked up media attachment {item}.',
  },
  moved: {
    id: 'upload_form.drag_and_drop.on_drag_over',
    defaultMessage: 'Media attachment {item} was moved.',
  },
  dropped: {
    id: 'upload_form.drag_and_drop.on_drag_end',
    defaultMessage: 'Media attachment {item} was dropped.',
  },
  cancelled: {
    id: 'upload_form.drag_and_drop.on_drag_cancel',
    defaultMessage: 'Dragging was cancelled. Media attachment {item} was not moved.',
  },
});

const UploadForm = ({ mediaIds, onMove }) => {
  const intl = useIntl();
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 150, tolerance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const accessibility = useMemo(() => ({
    screenReaderInstructions: {
      draggable: intl.formatMessage(messages.instructions),
    },
    announcements: {
      onDragStart: ({ active }) => intl.formatMessage(messages.pickedUp, { item: mediaIds.indexOf(active.id) + 1 }),
      onDragOver: ({ active, over }) => intl.formatMessage(messages.moved, { item: (over ? mediaIds.indexOf(over.id) : mediaIds.indexOf(active.id)) + 1 }),
      onDragEnd: ({ active, over }) => intl.formatMessage(messages.dropped, { item: (over ? mediaIds.indexOf(over.id) : mediaIds.indexOf(active.id)) + 1 }),
      onDragCancel: ({ active }) => intl.formatMessage(messages.cancelled, { item: mediaIds.indexOf(active.id) + 1 }),
    },
  }), [intl, mediaIds]);

  const handleDragEnd = useCallback(({ active, over }) => {
    if (over && active.id !== over.id) {
      onMove(active.id, over.id);
    }
  }, [onMove]);

  return (
    <div className='compose-form__upload-wrapper'>
      <UploadProgressContainer />
      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        onDragEnd={handleDragEnd}
        accessibility={accessibility}
      >
        <SortableContext items={mediaIds.toArray()} strategy={rectSortingStrategy}>
          <div className='compose-form__uploads-wrapper'>
            {mediaIds.map((id, index) => <UploadContainer id={id} index={index} key={id} />)}
          </div>
        </SortableContext>
      </DndContext>

      {!mediaIds.isEmpty() && <SensitiveButtonContainer />}
    </div>
  );
};

UploadForm.propTypes = {
  mediaIds: ImmutablePropTypes.list.isRequired,
  onMove: PropTypes.func.isRequired,
};

export default UploadForm;
