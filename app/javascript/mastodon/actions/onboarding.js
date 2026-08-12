import { changeSetting, saveSettings } from './settings';

export const INTRODUCTION_VERSION = 20181216044202;

export const closeOnboarding = () => dispatch => {
  dispatch(changeSetting(['introductionVersion'], INTRODUCTION_VERSION));
  dispatch(changeSetting(['onboarding', 'completed'], true));
  dispatch(changeSetting(['onboarding', 'last_step'], null));
  dispatch(saveSettings());
};
