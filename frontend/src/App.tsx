import ServerControls from "./components/ServerControl";
import Header from "./components/Header";
import Footer from "./components/Footer";
import ContentWrapper from "./components/ContentWrapper";
import MessageWrapper from "./components/MessageWrapper";

function App() {
    return (
        <div id="App">
            <Header />
            <MessageWrapper />
            <ServerControls />
            <ContentWrapper />
            <Footer />
        </div>
    )
}

export default App
