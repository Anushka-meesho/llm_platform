import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button, Typography } from '@meesho/merlin-ui-tailwind';

// ErrorBoundary catches render-time exceptions anywhere below it so a single
// component crash shows a recoverable message instead of white-screening the
// whole app. The error + component stack are logged to the console for debugging.
interface Props {
  children: ReactNode;
}
interface State {
  error: Error | null;
}

class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Unhandled UI error:', error, info.componentStack);
  }

  handleReload = () => {
    this.setState({ error: null });
    window.location.reload();
  };

  render() {
    if (this.state.error) {
      return (
        <div className="flex h-screen flex-col items-center justify-center gap-4 bg-secondary-bg p-6 text-center">
          <Typography variant="heading" size="4" className="text-primary-text">
            Something went wrong
          </Typography>
          <Typography variant="body" size="3" className="max-w-lg text-tertiary-text">
            The page hit an unexpected error and couldn't render. Reloading usually fixes it.
          </Typography>
          <pre className="max-w-lg overflow-auto rounded border border-solid border-error-border bg-error-bg px-3 py-2 text-left text-xs text-error-text">
            {this.state.error.message}
          </pre>
          <Button variant="primary" onClick={this.handleReload}>
            Reload
          </Button>
        </div>
      );
    }
    return this.props.children;
  }
}

export default ErrorBoundary;
