import { createTheme } from '@mui/material';
import styles from './assets/style/variables.module.scss';

// @ts-ignore
export const theme= createTheme({
  palette: {
    primary: {
      main: styles.primaryColor,
      light: styles.primaryColorLight,
      dark: styles.primaryColorDark,
    },
    secondary: {
      main: styles.secondaryColor,
      light: styles.secondaryColorLight,
      dark: styles.secondaryColorDark,
    },
    error: {
        main: styles.errorColor,
    },
    warning: {
        main: styles.warningColor,
    },
    info: {
        main: styles.infoColor,
    },
    success: {
        main: styles.successColor,
    },
    divider: styles.dividerColor,
    background: {
      default: styles.backgroundColor,
      paper: styles.backgroundColorPaper,
    },
    text: {
      primary: styles.primaryTextColor,
      secondary: styles.secondaryTextColor,
      disabled: styles.disabledTextColor,
    }
  },
});